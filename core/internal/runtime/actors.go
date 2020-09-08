package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.ibm.com/solsa/kar.git/core/internal/config"
	"github.ibm.com/solsa/kar.git/core/internal/pubsub"
	"github.ibm.com/solsa/kar.git/core/pkg/logger"
)

// Actor uniquely identifies an actor instance.
type Actor struct {
	Type string // actor type
	ID   string // actor instance id
}

// MapOp describes the requested map operation on an Actors state
type MapOp struct {
	Op string `json:"op"`
	Updates map[string]interface{} `json:"updates,omitempty"`
}

type actorEntry struct {
	actor   Actor
	time    time.Time     // last release time
	lock    chan struct{} // entry lock, never held for long, no need to watch ctx.Done()
	valid   bool          // false iff entry has been removed from table
	session string        // current session or "" if none
	depth   int           // session depth
	busy    chan struct{} // close to notify end of session
}

var (
	actorTable             = sync.Map{} // actor table: Actor -> *actorEntry
	errActorHasMoved       = errors.New("actor has moved")
	errActorAcquireTimeout = errors.New("timeout occured while acquiring actor")
)

// acquire locks the actor, session must be not be ""
// "exclusive" and "reminder" are reserved session names
// acquire returns true if actor requires activation before invocation
func (actor Actor) acquire(ctx context.Context, session string) (*actorEntry, bool, error) {
	e := &actorEntry{actor: actor, lock: make(chan struct{}, 1)}
	e.lock <- struct{}{} // lock entry
	for {
		if v, loaded := actorTable.LoadOrStore(actor, e); loaded {
			e := v.(*actorEntry) // found existing entry, := is required here!
			e.lock <- struct{}{} // lock entry
			if e.valid {
				if e.session == "" { // start new session
					e.session = session
					e.depth = 1
					e.busy = make(chan struct{})
					<-e.lock
					return e, false, nil
				}
				if session == "reminder" || session != "exclusive" && session == e.session { // reenter existing session
					e.depth++
					<-e.lock
					return e, false, nil
				}
				// another session is in progress
				busy := e.busy // read while holding the lock
				<-e.lock
				select {
				case <-busy: // wait
				case <-ctx.Done():
					return nil, false, ctx.Err()
				case <-time.After(config.ActorTimeout):
					return nil, false, errActorAcquireTimeout
				}
				// loop around
				// no fairness issue trying to reacquire because we waited on busy
			} else {
				<-e.lock // invalid entry
				// loop around
				// no fairness issue trying to reacquire because this entry is dead
			}
		} else { // new entry
			sidecar, err := pubsub.GetSidecar(actor.Type, actor.ID)
			if err != nil {
				<-e.lock
				return nil, false, err
			}
			if sidecar == config.ID { // start new session
				e.valid = true
				e.session = session
				e.depth = 1
				e.busy = make(chan struct{})
				<-e.lock
				return e, true, nil
			}
			actorTable.Delete(actor)
			<-e.lock // actor has moved
			return nil, false, errActorHasMoved
		}
	}
}

// release releases the actor lock
// release updates the timestamp if the actor was invoked
// release removes the actor from the table if it was not activated at depth 0
func (e *actorEntry) release(session string, invoked bool) {
	e.lock <- struct{}{} // lock entry
	e.depth--
	if invoked {
		e.time = time.Now() // update last release time
	}
	if e.depth == 0 { // end session
		if !invoked { // actor was not activated
			e.valid = false
			actorTable.Delete(e.actor)
		}
		e.session = ""
		close(e.busy)
	}
	<-e.lock
}

// collect deactivates actors that not been used since time
func collect(ctx context.Context, time time.Time) error {
	actorTable.Range(func(actor, v interface{}) bool {
		e := v.(*actorEntry)
		select {
		case e.lock <- struct{}{}: // try acquire
			if e.valid && e.session == "" && e.time.Before(time) {
				e.depth = 1
				e.session = "exclusive"
				e.busy = make(chan struct{})
				<-e.lock
				err := deactivate(ctx, actor.(Actor))
				e.lock <- struct{}{}
				e.depth--
				e.session = ""
				if err == nil {
					e.valid = false
					actorTable.Delete(actor)
				}
				close(e.busy)
			}
			<-e.lock
		default:
		}
		return ctx.Err() == nil // stop collection if cancelled
	})
	return ctx.Err()
}

// Returns a json map of actor types ->  list of active IDs on a per-sidecar basis
func GetActors() (string, error) {
	information := make(map[string][]string)
	actorTable.Range(func(actor, v interface{}) bool {
		e := v.(*actorEntry)
		e.lock <- struct{}{}
		if e.valid {
			information[e.actor.Type] = append(information[e.actor.Type], e.actor.ID)
		}
		<-e.lock
		return true
	})
	m, err := json.Marshal(information)
	if err != nil {
		logger.Debug("Error marshaling actor information data: %v", err)
		return "", err
	}
	return string(m), nil
}

// Returns map of actor types ->  list of active IDs for all sidecars in the app
func GetAllActors(ctx context.Context, format string) (string, error) {
	information := make(map[string][]string)
	var err error
	for _, sidecar := range pubsub.Sidecars() {
		var actorInfo string
		var actorReply *Reply
		var actorInformation map[string][]string
		if sidecar != config.ID {
			// Make call to other sidecar, returns the result of GetActors() there
			msg := map[string]string{
				"protocol": "sidecar",
				"sidecar":  sidecar,
				"command":  "getActors",
			}
			actorReply, err = callHelper(ctx, msg, false)
			if err != nil || actorReply.StatusCode != 200 {
				logger.Debug("Error gathering actor information: %v", err)
				return "", err
			}
			actorInfo = actorReply.Payload
		} else {
			actorInfo, err = GetActors()
		}
		err = json.Unmarshal([]byte(actorInfo), &actorInformation)
		if err != nil {
			logger.Debug("Error unmarshaling actor information: %v", err)
			return "", err
		}
		for actorType, actorIDs := range actorInformation { // accumulate sidecar's info into information
			information[actorType] = append(information[actorType], actorIDs...)
		}
	}
	//TODO: Sort IDs ?
	if format == "json" || format == "application/json" {
		var m []byte
		m, err = json.Marshal(information)
		if err != nil {
			logger.Debug("Error marshaling actors information: %v", err)
			return "", err
		}
		return string(m), nil
	} else {
		var str strings.Builder
		fmt.Fprint(&str, "\nActor Type\n : IDs of actors with type\n")
		for actorType, actorIDs := range information {
			fmt.Fprintf(&str, "%v\n : ", actorType)
			if len(actorIDs) > 10 { // Display up to 10 IDs per actor.
				fmt.Fprint(&str, "[")
				for i := 0; i < 10; i++ {
					fmt.Fprintf(&str, "%v ", actorIDs[i])
				}
				fmt.Fprintf(&str, "... and %v more]\n", len(actorIDs)-10)
			} else {
				fmt.Fprintf(&str, "%v\n", actorIDs)
			}
		}
		return str.String(), nil
	}
}

// migrate releases the actor lock and updates the sidecar for the actor
// the lock cannot be held multiple times
func (e *actorEntry) migrate(sidecar string) error {
	e.lock <- struct{}{}
	e.depth--
	e.session = ""
	e.valid = false
	actorTable.Delete(e.actor)
	_, err := pubsub.CompareAndSetSidecar(e.actor.Type, e.actor.ID, config.ID, sidecar)
	close(e.busy)
	<-e.lock
	return err
}