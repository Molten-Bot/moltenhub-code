package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

const a2aResultPartMediaType = "application/json"

func (c APIClient) publishResultA2A(ctx context.Context, token string, payload map[string]any) error {
	normalizedToken, err := requireHubToken(token, "publish result over a2a")
	if err != nil {
		return err
	}
	metadata := publishResultA2ARoutingMetadata(payload)
	if len(metadata) == 0 {
		return fmt.Errorf("publish result over a2a requires target agent")
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode a2a result payload: %w", err)
	}
	part := a2a.NewTextPart(string(encoded))
	part.MediaType = a2aResultPartMediaType
	msg := a2a.NewMessage(a2a.MessageRoleAgent, part)
	if requestID := firstString(payload["request_id"]); requestID != "" {
		msg.ID = requestID
	}
	msg.Metadata = metadata

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 35 * time.Second}
	}
	client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{
		a2a.NewAgentInterface(c.a2aEndpointURL(), a2a.TransportProtocolJSONRPC),
	}, a2aclient.WithJSONRPCTransport(httpClient))
	if err != nil {
		return fmt.Errorf("create a2a client: %w", err)
	}

	callCtx := a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams{
		"Authorization": []string{"Bearer " + normalizedToken},
	})
	_, sendErr := client.SendMessage(callCtx, &a2a.SendMessageRequest{
		Config: &a2a.SendMessageConfig{
			ReturnImmediately: true,
		},
		Message:  msg,
		Metadata: metadata,
	})
	destroyErr := client.Destroy()
	if sendErr != nil {
		return errors.Join(fmt.Errorf("a2a SendMessage: %w", sendErr), destroyErr)
	}
	if destroyErr != nil {
		return fmt.Errorf("destroy a2a client: %w", destroyErr)
	}
	return nil
}

func (c APIClient) a2aEndpointURL() string {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/") + "/a2a"
}

func publishResultA2ARoutingMetadata(payload map[string]any) map[string]any {
	_, routed := publishResultOpenClawBody(payload)
	if !routed {
		return nil
	}
	metadata := map[string]any{}
	if toAgentURI := firstString(payload["to_agent_uri"]); toAgentURI != "" {
		metadata["to_agent_uri"] = toAgentURI
	} else if toAgentUUID := firstString(payload["to_agent_uuid"]); toAgentUUID != "" {
		metadata["to_agent_uuid"] = toAgentUUID
	} else if routeTarget := firstString(payload["to"], payload["reply_to"]); routeTarget != "" {
		if looksLikeAgentURI(routeTarget) {
			metadata["to_agent_uri"] = routeTarget
		} else {
			metadata["to_agent_uuid"] = routeTarget
		}
	}
	return metadata
}

func publishResultOpenClawBody(payload map[string]any) (map[string]any, bool) {
	body := map[string]any{
		"message": payload,
	}
	routed := false
	if toAgentURI := firstString(payload["to_agent_uri"]); toAgentURI != "" {
		body["to_agent_uri"] = toAgentURI
		routed = true
	} else if toAgentUUID := firstString(payload["to_agent_uuid"]); toAgentUUID != "" {
		body["to_agent_uuid"] = toAgentUUID
		routed = true
	} else if routeTarget := firstString(payload["to"], payload["reply_to"]); routeTarget != "" {
		if looksLikeAgentURI(routeTarget) {
			body["to_agent_uri"] = routeTarget
		} else {
			body["to_agent_uuid"] = routeTarget
		}
		routed = true
	}
	if requestID := firstString(payload["request_id"]); requestID != "" {
		body["client_msg_id"] = requestID
	}
	return body, routed
}
