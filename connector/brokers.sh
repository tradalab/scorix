#!/usr/bin/env bash
set -euo pipefail

down() {
  docker rm -f scorix-test-redis scorix-test-nats scorix-test-kafka >/dev/null 2>&1 || true
}

if [[ "${1:-up}" == "down" ]]; then
  down
  exit 0
fi

down
docker run -d --name scorix-test-redis -p 127.0.0.1:56379:6379 redis:8.2-alpine >/dev/null
docker run -d --name scorix-test-nats -p 127.0.0.1:54222:4222 nats:2.15.2-alpine --user scorix --pass right-pass >/dev/null
docker run -d --name scorix-test-kafka -p 127.0.0.1:59092:9092 -p 127.0.0.1:59094:9094 \
  -e KAFKA_OPTS=-Djava.security.auth.login.config=/tmp/jaas.conf \
  -e KAFKA_NODE_ID=1 -e KAFKA_PROCESS_ROLES=broker,controller \
  -e KAFKA_LISTENERS=PLAINTEXT://:9092,SASL_PLAINTEXT://:9094,CONTROLLER://:9093,INTERNAL://:9095 \
  -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://127.0.0.1:59092,SASL_PLAINTEXT://127.0.0.1:59094,INTERNAL://localhost:9095 \
  -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER -e KAFKA_INTER_BROKER_LISTENER_NAME=INTERNAL \
  -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,SASL_PLAINTEXT:SASL_PLAINTEXT,INTERNAL:PLAINTEXT \
  -e KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093 -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
  -e KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS=0 -e KAFKA_SASL_ENABLED_MECHANISMS=PLAIN,SCRAM-SHA-256,SCRAM-SHA-512 \
  --entrypoint sh apache/kafka:4.3.1 -c \
  'printf "KafkaServer {\n org.apache.kafka.common.security.plain.PlainLoginModule required user_scorix=\"right-pass\";\n org.apache.kafka.common.security.scram.ScramLoginModule required;\n};\n" > /tmp/jaas.conf && exec /__cacert_entrypoint.sh /etc/kafka/docker/run' >/dev/null

for _ in $(seq 60); do
  if docker logs scorix-test-kafka 2>&1 | grep -q "Kafka Server started"; then
    for mechanism in SCRAM-SHA-256 SCRAM-SHA-512; do
      docker exec scorix-test-kafka /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9095 --alter \
        --add-config "$mechanism=[password=right-pass]" --entity-type users --entity-name scorix >/dev/null
    done
    for _ in $(seq 30); do
      docker exec scorix-test-kafka /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9095 --describe \
        --entity-type users --entity-name scorix 2>/dev/null | grep -q SCRAM-SHA-512 && break
      sleep 1
    done
    cat <<ENV
SCORIX_TEST_REDIS=127.0.0.1:56379
SCORIX_TEST_NATS=nats://127.0.0.1:54222
SCORIX_TEST_KAFKA=127.0.0.1:59092
SCORIX_TEST_KAFKA_SASL=127.0.0.1:59094
ENV
    exit 0
  fi
  sleep 2
done
docker logs scorix-test-kafka
exit 1
