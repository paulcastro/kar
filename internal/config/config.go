// Package config loads the runtime configuration
package config

import (
	"flag"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.ibm.com/solsa/kar.git/pkg/logger"
)

// Separator character for store keys and topic names
const Separator = "_" // must not be a legal DNS name character

var (
	// AppName is the name of the application
	AppName string

	// ServiceName is the name of the service
	ServiceName string

	// ActorTypes are the actor types implemented by this service
	ActorTypes []string

	// ActorReminderInterval is the interval at which reminders are processed
	ActorReminderInterval time.Duration

	// ActorReminderAcceptableDelay controls the threshold at which reminders are logged as being late
	ActorReminderAcceptableDelay time.Duration

	// ServicePort is the HTTP port the service will be listening on
	ServicePort int

	// RuntimePort is the HTTP port the runtime will be listening on
	RuntimePort int

	// KubernetesMode is true when this process is running in a sidecar container in a Kubernetes Pod
	KubernetesMode bool

	// KafkaBrokers is an array of Kafka brokers
	KafkaBrokers []string

	// KafkaEnableTLS is set if the Kafka connection requires TLS
	KafkaEnableTLS bool

	// KafkaUsername is the username for SASL authentication (optional)
	KafkaUsername string

	// KafkaPassword is the password for SASL authentication (optional)
	KafkaPassword string

	// KafkaVersion is the expected Kafka version
	KafkaVersion string

	// RedisHost is the host of the Redis instance
	RedisHost string

	// RedisPort is the port of the Redis instance
	RedisPort int

	// RedisEnableTLS is set if the Redis connection requires TLS
	RedisEnableTLS bool

	// RedisPassword the the password of the Redis instance (optional)
	RedisPassword string

	// ID is the unique id of this sidecar instance
	ID = uuid.New().String()

	// Enable h2c
	H2C bool
)

func init() {
	var kafkaBrokers, verbosity, configDir, actorTypes, remindInterval, remindDelay string
	var err error

	flag.StringVar(&AppName, "app", "", "The name of the application")
	flag.StringVar(&ServiceName, "service", "", "The name of the service being joined to the application")
	flag.StringVar(&actorTypes, "actors", "", "The actor types implemented by this service, as a comma separated list")
	flag.StringVar(&remindInterval, "actor_reminder_interval", "100ms", "Actor reminder processing interval (default 100ms)")
	flag.StringVar(&remindDelay, "actor_reminder_acceptable_delay", "3s", "Threshold at which reminders are logged as being late")
	flag.IntVar(&ServicePort, "send", 8080, "The service port")
	flag.IntVar(&RuntimePort, "recv", 0, "The runtime port")
	flag.BoolVar(&KubernetesMode, "kubernetes_mode", false, "Running as a sidecar container in a Kubernetes Pod")
	flag.StringVar(&kafkaBrokers, "kafka_brokers", "", "The Kafka brokers to connect to, as a comma separated list")
	flag.BoolVar(&KafkaEnableTLS, "kafka_enable_tls", false, "Use TLS to communicate with Kafka")
	flag.StringVar(&KafkaUsername, "kafka_username", "", "The SASL username if any")
	flag.StringVar(&KafkaPassword, "kafka_password", "", "The SASL password if any")
	flag.StringVar(&KafkaVersion, "kafka_version", "", "Kafka cluster version")
	flag.StringVar(&RedisHost, "redis_host", "", "The Redis host")
	flag.IntVar(&RedisPort, "redis_port", 0, "The Redis port")
	flag.BoolVar(&RedisEnableTLS, "redis_enable_tls", false, "Use TLS to communicate with Redis")
	flag.StringVar(&RedisPassword, "redis_password", "", "The password of the Redis server if any")
	flag.StringVar(&verbosity, "v", "error", "Logging verbosity")
	flag.StringVar(&configDir, "config_dir", "", "Directory containing configuration files")
	flag.BoolVar(&H2C, "h2c", false, "Use h2c to communicate with service")

	flag.Parse()

	logger.SetVerbosity(verbosity)

	if AppName == "" {
		logger.Fatal("app name is required")
	}

	if ServiceName == "" {
		logger.Fatal("service name is required")
	}

	if actorTypes == "" {
		ActorTypes = []string{}
	} else {
		ActorTypes = strings.Split(actorTypes, ",")
	}

	ActorReminderInterval, err = time.ParseDuration(remindInterval)
	if err != nil {
		logger.Fatal("error parsing actor_reminder_interval %s", remindInterval)
	}

	ActorReminderAcceptableDelay, err = time.ParseDuration(remindDelay)
	if err != nil {
		logger.Fatal("error parsing actor_reminder_acceptable_delay %s", remindDelay)
	}

	if !KafkaEnableTLS && os.Getenv("KAFKA_ENABLE_TLS") != "" {
		if KafkaEnableTLS, err = strconv.ParseBool(os.Getenv("KAFKA_ENABLE_TLS")); err != nil {
			logger.Fatal("error parsing environment variable KAFKA_ENABLE_TLS")
		}
	}

	if kafkaBrokers == "" {
		if kafkaBrokers = os.Getenv("KAFKA_BROKERS"); kafkaBrokers == "" {
			if kafkaBrokers = loadStringFromConfig(configDir, "kafka_brokers"); kafkaBrokers == "" {
				logger.Fatal("at least one Kafka broker is required")
			}
		}
	}

	KafkaBrokers = strings.Split(kafkaBrokers, ",")

	if KafkaUsername == "" {
		if KafkaUsername = os.Getenv("KAFKA_USERNAME"); KafkaUsername == "" {
			if KafkaUsername = loadStringFromConfig(configDir, "kafka_username"); KafkaUsername == "" {
				KafkaUsername = "token"
			}
		}
	}

	if KafkaPassword == "" {
		if KafkaPassword = os.Getenv("KAFKA_PASSWORD"); KafkaPassword == "" {
			KafkaPassword = loadStringFromConfig(configDir, "kafka_password")
		}
	}

	if KafkaVersion == "" {
		if KafkaVersion = os.Getenv("KAFKA_VERSION"); KafkaVersion == "" {
			if KafkaVersion = loadStringFromConfig(configDir, "kafka_version"); KafkaVersion == "" {
				KafkaVersion = "2.2.0"
			}
		}
	}

	if !RedisEnableTLS && os.Getenv("REDIS_ENABLE_TLS") != "" {
		if RedisEnableTLS, err = strconv.ParseBool(os.Getenv("REDIS_ENABLE_TLS")); err != nil {
			logger.Fatal("error parsing environment variable REDIS_ENABLE_TLS")
		}
	}

	if RedisHost == "" {
		if RedisHost = os.Getenv("REDIS_HOST"); RedisHost == "" {
			if RedisHost = loadStringFromConfig(configDir, "redis_host"); RedisHost == "" {
				logger.Fatal("Redis host is required")
			}
		}
	}

	if RedisPort == 0 {
		if os.Getenv("REDIS_PORT") != "" {
			if RedisPort, err = strconv.Atoi(os.Getenv("REDIS_PORT")); err != nil {
				logger.Fatal("error parsing environment variable REDIS_PORT")
			}
		} else {
			if rp := loadStringFromConfig(configDir, "redis_port"); rp != "" {
				if RedisPort, err = strconv.Atoi(rp); err != nil {
					logger.Fatal("error parsing config value for redis_port: %s", rp)
				}
			} else {
				RedisPort = 6379
			}
		}
	}

	if RedisPassword == "" {
		if RedisPassword = os.Getenv("REDIS_PASSWORD"); RedisPassword == "" {
			RedisPassword = loadStringFromConfig(configDir, "redis_password")
		}
	}
}

func loadStringFromConfig(path string, file string) string {
	value := ""
	if path != "" {
		if bytes, err := ioutil.ReadFile(filepath.Join(path, file)); err == nil {
			value = string(bytes)
		}
	}
	return value
}
