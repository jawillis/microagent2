package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	rdb *redis.Client
}

func NewClient(addr string) *Client {
	return &Client{
		rdb: redis.NewClient(&redis.Options{Addr: addr}),
	}
}

func (c *Client) Close() error {
	return c.rdb.Close()
}

func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

func (c *Client) Redis() *redis.Client {
	return c.rdb
}

func (c *Client) EnsureGroup(ctx context.Context, stream, group string) error {
	err := c.rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return err
	}
	return nil
}

func (c *Client) Publish(ctx context.Context, stream string, msg *Message) (string, error) {
	fields, err := msg.Encode()
	if err != nil {
		return "", err
	}
	return c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: fields,
	}).Result()
}

func (c *Client) ReadGroup(ctx context.Context, stream, group, consumer string, count int64, block time.Duration) ([]*Message, []string, error) {
	results, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if err != nil {
		return nil, nil, err
	}

	var msgs []*Message
	var ids []string
	for _, r := range results {
		for _, m := range r.Messages {
			msg, decErr := DecodeFromStream(m.Values)
			if decErr != nil {
				continue
			}
			msgs = append(msgs, msg)
			ids = append(ids, m.ID)
		}
	}
	return msgs, ids, nil
}

func (c *Client) Ack(ctx context.Context, stream, group string, ids ...string) error {
	return c.rdb.XAck(ctx, stream, group, ids...).Err()
}

func (c *Client) PubSubPublish(ctx context.Context, channel string, msg *Message) error {
	data, err := msg.Encode()
	if err != nil {
		return err
	}
	return c.rdb.Publish(ctx, channel, data["data"]).Err()
}

func (c *Client) PubSubSubscribe(ctx context.Context, channels ...string) *Subscription {
	sub := c.rdb.Subscribe(ctx, channels...)
	return &Subscription{sub: sub}
}

type Subscription struct {
	sub *redis.PubSub
}

func (s *Subscription) Channel() <-chan *redis.Message {
	return s.sub.Channel()
}

func (s *Subscription) Close() error {
	return s.sub.Close()
}

func (s *Subscription) ReceiveMessage(ctx context.Context) (*Message, error) {
	redisMsg, err := s.sub.ReceiveMessage(ctx)
	if err != nil {
		return nil, err
	}
	var msg Message
	if err := decodeJSON(redisMsg.Payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *Client) WaitForReply(ctx context.Context, replyStream, correlationID string, timeout time.Duration) (*Message, error) {
	group := fmt.Sprintf("cg:reply:%s", correlationID)
	consumer := "waiter"

	if err := c.EnsureGroup(ctx, replyStream, group); err != nil {
		return nil, err
	}

	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return nil, ErrTimeout
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			msgs, ids, err := c.ReadGroup(ctx, replyStream, group, consumer, 1, time.Second)
			if err != nil {
				if err == redis.Nil {
					continue
				}
				return nil, err
			}
			for i, msg := range msgs {
				if msg.CorrelationID == correlationID {
					_ = c.Ack(ctx, replyStream, group, ids[i])
					return msg, nil
				}
				_ = c.Ack(ctx, replyStream, group, ids[i])
			}
		}
	}
}
