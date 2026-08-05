package outbox

import (
	"context"
	"fmt"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
)

// RocketMQPublisher pushes events to a topic. The event id is used as the
// message key so the consumer can deduplicate.
type RocketMQPublisher struct {
	producer rocketmq.Producer
	topic    string
}

func NewRocketMQPublisher(cfg config.NotifyConfig) (*RocketMQPublisher, error) {
	p, err := rocketmq.NewProducer(
		producer.WithNameServer(cfg.RocketMQ.NameServer),
		producer.WithGroupName(cfg.RocketMQ.Group),
		producer.WithRetry(2),
	)
	if err != nil {
		return nil, fmt.Errorf("rocketmq: new producer: %w", err)
	}
	if err := p.Start(); err != nil {
		return nil, fmt.Errorf("rocketmq: start producer: %w", err)
	}
	return &RocketMQPublisher{producer: p, topic: cfg.RocketMQ.Topic}, nil
}

func (p *RocketMQPublisher) Name() string { return "rocketmq" }

func (p *RocketMQPublisher) Publish(ctx context.Context, event *model.NotifyOutbox) error {
	msg := primitive.NewMessage(p.topic, []byte(event.Payload))
	msg.WithKeys([]string{event.EventID})
	msg.WithTag(event.EventType)
	_, err := p.producer.SendSync(ctx, msg)
	return err
}

func (p *RocketMQPublisher) Close() error { return p.producer.Shutdown() }
