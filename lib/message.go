package lib

import (
	"context"
	"fmt"

	"cloud.google.com/go/pubsub"
)

// SetupMsg generates the communication channel with PubSub. Subsequent use is to write
// messages passed from the VM instances.
func (p *Project) SetupMsg(ctx context.Context) MachineResponse {
	client, err := pubsub.NewClient(ctx, p.ProjectID)
	if err != nil {
		return MachineResponse{err.Error()}
	}

	// Create the message topic.
	p.MsgTopic, err = client.CreateTopic(ctx, "Gelato")
	if err != nil {
		return MachineResponse{err.Error()}
	}

	return MachineResponse{"Success!"}
}

// PublishMsg passes the name and IP address of the master and slave instances
// to PubSub after they have been spun up by GPC.
func (p *Project) PublishMsg(ctx context.Context, msg string) error {
	result := p.MsgTopic.Publish(ctx, &pubsub.Message{
		Data: []byte(msg),
	})

	// Block until the result is returned and a server-generated
	// ID is returned for the published message.
	id, err := result.Get(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("Published a message; msg ID: %v\n", id)

	return nil
}
