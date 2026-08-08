package outbox

import (
	"context"
	"fmt"
	"net"
	"strings"

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
	nameServers, err := resolveNameServers(cfg.RocketMQ.NameServer)
	if err != nil {
		return nil, err
	}
	p, err := rocketmq.NewProducer(
		producer.WithNameServer(nameServers),
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

// resolveNameServers turns "host:port" entries into "ip:port": the client
// validates every name server against an IP regex and rejects DNS names, which
// is what docker compose service names (rocketmq-namesrv:9876) are.
func resolveNameServers(addrs []string) ([]string, error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("rocketmq: notify.rocketmq.name_server is empty")
	}
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		// A namesrv discovery URL is passed through untouched.
		if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
			out = append(out, addr)
			continue
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("rocketmq: name server %q: %w", addr, err)
		}
		if net.ParseIP(host) != nil {
			out = append(out, addr)
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("rocketmq: resolve name server %q: %w", addr, err)
		}
		resolved := ""
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				resolved = v4.String()
				break
			}
		}
		if resolved == "" && len(ips) > 0 {
			resolved = ips[0].String()
		}
		if resolved == "" {
			return nil, fmt.Errorf("rocketmq: name server %q resolved to no address", addr)
		}
		out = append(out, net.JoinHostPort(resolved, port))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("rocketmq: notify.rocketmq.name_server is empty")
	}
	return out, nil
}
