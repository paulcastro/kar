package pubsub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"math/rand"
	"strconv"
	"sync"

	"github.com/Shopify/sarama"
	"github.com/cenkalti/backoff/v4"
	"github.ibm.com/solsa/kar.git/internal/config"
	"github.ibm.com/solsa/kar.git/internal/store"
	"github.ibm.com/solsa/kar.git/pkg/logger"
)

type userData struct {
	Sidecar string                       // id of this sidecar
	Service string                       // name of this service
	Live    map[int32]map[int64]struct{} // live offsets
}

var (
	conf     = sarama.NewConfig()
	admin    sarama.ClusterAdmin
	producer sarama.SyncProducer
	consumer sarama.ConsumerGroup
	topic    = "kar" + config.Separator + config.AppName

	// output channel
	out = make(chan Message) // TODO multiple channels?

	// routes
	replicas map[string][]string // map services to sidecars
	routes   map[string][]int32  // map sidecards to partitions
	mu       = sync.RWMutex{}    // synchronize changes to routes

	// termination
	ctx context.Context

	// state of this sidecar
	here = userData{
		Sidecar: config.ID,
		Service: config.ServiceName,
		Live:    map[int32]map[int64]struct{}{},
	}

	// local progress
	local = map[int32]map[int64]struct{}{} // offsets started or finished by this sidecar
	lock  = sync.Mutex{}                   // lock to protect local and here.Live

	// global progress (no lock needed)
	live map[int32]map[int64]struct{}     // live offsets at beginning of session
	done = map[int32]map[int64]struct{}{} // done offsets at beginning of session
)

// Message is the type of messages
type Message struct {
	Value     map[string]string
	Valid     bool
	partition int32
	offset    int64
}

func marshal(ud userData) []byte {
	lock.Lock()
	b, err := json.Marshal(ud)
	lock.Unlock()
	if err != nil {
		logger.Fatal("failed to marshal userData: %v", err)
	}
	return b
}

func mangle(p int32) string {
	return "partition" + config.Separator + strconv.Itoa(int(p))
}

// Mark must be called when a message has been processed
func (msg *Message) Mark() {
	logger.Debug("finishing partition %d offset %d", msg.partition, msg.offset)
	_, err := store.ZAdd(mangle(msg.partition), msg.offset, strconv.FormatInt(msg.offset, 10)) // tell store offset is done
	if err != nil {
		logger.Error("redis error: %v", err)
	}
	lock.Lock()
	delete(here.Live[msg.partition], msg.offset)
	lock.Unlock()
}

// Confirm must be called before processing a message
func (msg *Message) Confirm() bool {
	lock.Lock()
	if local[msg.partition] == nil { // new partition
		local[msg.partition] = map[int64]struct{}{}
		here.Live[msg.partition] = map[int64]struct{}{}
	}
	if _, ok := local[msg.partition][msg.offset]; ok { // already processed or in progress
		lock.Unlock()
		logger.Debug("skipping partition %d offset %d", msg.partition, msg.offset)
		return false
	}
	here.Live[msg.partition][msg.offset] = struct{}{}
	local[msg.partition][msg.offset] = struct{}{}
	lock.Unlock()
	logger.Debug("starting to process partition %d offset %d", msg.partition, msg.offset)
	return true
}

// routeToService maps a service to a partition (keep trying)
func routeToService(service string) (partition int32, sidecar string, err error) {
	err = backoff.Retry(func() error { // keep trying
		mu.RLock()
		sidecars := replicas[service]
		if len(sidecars) == 0 { // no sidecar matching this service
			mu.RUnlock()
			logger.Debug("no sidecar for service %s", service)
			return errors.New("no sidecar for service " + service)
		}
		sidecar = sidecars[rand.Int31n(int32(len(sidecars)))] // select random sidecar from list
		partition, err = routeToSidecar(sidecar)              // map sidecar to partition
		mu.RUnlock()
		return err
	}, backoff.WithContext(backoff.NewExponentialBackOff(), ctx)) // TODO adjust timeout
	return
}

// routeToSidecar maps a sidecar to a partition (no retries)
func routeToSidecar(sidecar string) (int32, error) {
	mu.RLock()
	partitions := routes[sidecar]
	mu.RUnlock()
	if len(partitions) == 0 { // no partition matching this sidecar
		logger.Debug("no partition for sidecar %s", sidecar)
		return -1, errors.New("no partition for sidecar " + sidecar)
	}
	return partitions[rand.Int31n(int32(len(partitions)))], nil // select random partition from list
}

// routeToSession maps a session of a service to a stable sidecar to a partition
// only switching to a new sidecar if the existing sidecar has died
func routeToSession(service string, session string) (partition int32, err error) {
	key := "session" + config.Separator + service + config.Separator + session
	var sidecar string
	for { // keep trying
		sidecar, err = store.Get(key) // retrieve already assigned sidecar if any
		if err != nil && err != store.ErrNil {
			return // redis error
		}
		if sidecar != "" { // no assigned sidecar yet
			_, _, err = routeToService(service) // make sure routes have been initialized
			if err != nil {
				return // no matching service, abort
			}
			partition, err = routeToSidecar(sidecar) // find partition for sidecar
			if err == nil {
				return // found sidecar and partition
			}
		}
		expected := sidecar                       // prepare to update assigned sidecar
		_, sidecar, err = routeToService(service) // wait for matching service
		if err != nil {
			return // no matching service, abort
		}
		_, err = store.CompareAndSet(key, expected, sidecar) // try saving sidecar
		if err != nil {
			return // redis error
		}
		// loop around
	}
}

// Send sends message to receiver
func Send(message map[string]string) error {
	msg, err := json.Marshal(message)
	if err != nil {
		logger.Debug("failed to marshal message with value %v: %v", message, err)
		return err
	}
	var partition int32
	switch message["protocol"] {
	case "service": // route to service
		if message["session"] != "" {
			partition, err = routeToSession(message["to"], message["session"])
		} else {
			partition, _, err = routeToService(message["to"])
		}
		if err != nil {
			logger.Debug("failed to route to service %s%s%s: %v", message["to"], config.Separator, message["session"], err)
			return err
		}
	case "sidecar": // route to sidecar
		partition, err = routeToSidecar(message["to"])
		if err != nil {
			logger.Debug("failed to route to sidecar %s: %v", message["to"], err)
			return err
		}
	}
	_, offset, err := producer.SendMessage(&sarama.ProducerMessage{
		Topic:     topic,
		Partition: partition,
		Value:     sarama.ByteEncoder(msg),
	})
	if err != nil {
		logger.Debug("failed to send message to partition %d: %v", partition, err)
		return err
	}
	logger.Debug("sent message on topic %s, at partition %d, offset %d, with value %s", topic, partition, offset, string(msg))
	return nil
}

// handler of consumer group session
type handler struct{}

// Setup consumer group session
func (consumer *handler) Setup(session sarama.ConsumerGroupSession) error {
	rp := map[string][]string{}           // temp replicas
	rt := map[string][]int32{}            // temp routes
	live = map[int32]map[int64]struct{}{} // clear live list
	done = map[int32]map[int64]struct{}{} // clear done list
	groups, err := admin.DescribeConsumerGroups([]string{topic})
	if err != nil {
		logger.Debug("failed to describe consumer group: %v", err)
		return err
	}
	members := groups[0].Members
	for memberID, member := range members {
		a, err := member.GetMemberAssignment()
		if err != nil {
			logger.Debug("failed to parse member assignment: %v", err)
			return err
		}
		m, err := member.GetMemberMetadata()
		if err != nil {
			logger.Debug("failed to parse member metadata: %v", err)
			return err
		}
		var there userData
		if err := json.Unmarshal(m.UserData, &there); err != nil {
			logger.Fatal("failed to unmarshal userdata: %v", err)
		}
		rp[there.Service] = append(rp[there.Service], there.Sidecar)
		rt[there.Sidecar] = append(rt[there.Sidecar], a.Topics[topic]...)
		for _, p := range session.Claims()[topic] { // for each partition assigned to this sidecar
			if live[p] == nil { // new partition
				live[p] = map[int64]struct{}{}
				done[p] = map[int64]struct{}{}
			}
			r, err := store.ZRange(mangle(p), 0, -1) // fetch done offsets from store
			if err != nil {
				logger.Error("redis error: %v", err)
			}
			for _, o := range r {
				k, _ := strconv.ParseInt(o, 10, 64)
				done[p][k] = struct{}{}
			}
			for k := range there.Live[p] {
				live[p][k] = struct{}{}
			}
		}
		if memberID == session.MemberID() {
			logger.Info("partitions: %v", a.Topics[topic])
		}
	}
	logger.Info("replicas: %v", rp)
	logger.Info("routes: %v", rt)
	logger.Info("userData: %v", here)
	mu.Lock()
	replicas = rp
	routes = rt
	mu.Unlock()
	return nil
}

// Cleanup consumer group session
func (consumer *handler) Cleanup(session sarama.ConsumerGroupSession) error {
	conf.Consumer.Group.Member.UserData = marshal(here)
	return nil
}

// ConsumeClaim processes messages of consumer claim
func (consumer *handler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	store.ZRemRangeByScore(mangle(claim.Partition()), 0, claim.InitialOffset()-1) // trim done list
	prefix := true                                                                // should advance cursor
	for msg := range claim.Messages() {
		logger.Debug("received message on topic %s, at partition %d, offset %d, with value %s", msg.Topic, msg.Partition, msg.Offset, msg.Value)
		if _, ok := live[msg.Partition][msg.Offset]; ok {
			prefix = false // stop advancing cursor
			logger.Debug("skipping live message on topic %s, at partition %d, offset %d, with value %s", msg.Topic, msg.Partition, msg.Offset, msg.Value)
			continue
		}
		if _, ok := done[msg.Partition][msg.Offset]; ok {
			if prefix {
				session.MarkMessage(msg, "") // advance cursor
			}
			logger.Debug("skipping done message on topic %s, at partition %d, offset %d, with value %s", msg.Topic, msg.Partition, msg.Offset, msg.Value)
			continue
		}
		prefix = false
		var message map[string]string
		err := json.Unmarshal(msg.Value, &message)
		if err != nil {
			logger.Error("failed to unmarshal message with value %s: %v", msg.Value, err)
			continue
		}
		valid := message["to"] == here.Sidecar || message["to"] == here.Service
		select {
		case <-session.Context().Done():
		case out <- Message{Value: message, Valid: valid, partition: msg.Partition, offset: msg.Offset}:
		}
	}
	return nil
}

// consume orchestrate the consumer group sessions
func consume() {
	for { // for each session
		if err := consumer.Consume(ctx, []string{topic}, &handler{}); err != nil {
			logger.Fatal("consumer error: %v", err)
		}
		if ctx.Err() != nil {
			close(out)
			return // consumer was cancelled
		}
		// next session
	}
}

// setContext sets the global context
func setContext(c context.Context) {
	ctx = c
}

// Dial establishes a connection to Kafka and returns a read channel from incoming messages
func Dial(ctx context.Context) <-chan Message {
	setContext(ctx)

	if version, err := sarama.ParseKafkaVersion(config.KafkaVersion); err != nil {
		logger.Fatal("invalid Kafka version: %v", err)
	} else {
		conf.Version = version
	}

	conf.ClientID = "kar"
	conf.Producer.Return.Successes = true
	conf.Producer.RequiredAcks = sarama.WaitForAll
	conf.Producer.Partitioner = sarama.NewManualPartitioner
	conf.Consumer.Offsets.Initial = sarama.OffsetOldest
	conf.Consumer.Group.Rebalance.Strategy = sarama.BalanceStrategyRange
	conf.Consumer.Group.Member.UserData = marshal(here)

	if config.KafkaPassword != "" {
		conf.Net.SASL.Enable = true
		conf.Net.SASL.User = config.KafkaUsername
		conf.Net.SASL.Password = config.KafkaPassword
		conf.Net.SASL.Handshake = true
		conf.Net.SASL.Mechanism = sarama.SASLTypePlaintext
	}

	if config.KafkaEnableTLS {
		conf.Net.TLS.Enable = true
		conf.Net.TLS.Config = &tls.Config{
			InsecureSkipVerify: true, // TODO certificates
		}
	}

	var err error

	admin, err = sarama.NewClusterAdmin(config.KafkaBrokers, conf)
	if err != nil {
		logger.Fatal("failed to create Kafka cluster admin: %v", err)
	}

	topics, err := admin.ListTopics()
	if err != nil {
		logger.Fatal("failed to list Kafka topics: %v", err)
	}
	if _, ok := topics[topic]; !ok {
		err = admin.CreateTopic(topic, &sarama.TopicDetail{NumPartitions: 10, ReplicationFactor: 3}, false) // TODO fix NumPartitions
		if err != nil {
			err = admin.CreateTopic(topic, &sarama.TopicDetail{NumPartitions: 10, ReplicationFactor: 1}, false)
		}
		if err != nil {
			logger.Fatal("failed to create Kafka topic: %v", err)
		}
	}

	producer, err = sarama.NewSyncProducer(config.KafkaBrokers, conf)
	if err != nil {
		logger.Fatal("failed to create Kafka producer: %v", err)
	}

	consumer, err = sarama.NewConsumerGroup(config.KafkaBrokers, topic, conf)
	if err != nil {
		logger.Fatal("failed to create Kafka consumer group: %v", err)
	}

	go consume()

	return out
}

// Close closes the connection to Kafka
func Close() {
	consumer.Close() // stop accepting incoming messages first
	producer.Close()
	admin.Close()
}
