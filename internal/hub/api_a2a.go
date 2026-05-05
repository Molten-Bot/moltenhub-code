package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	target, ok := publishResultA2ARouteTarget(payload)
	if !ok {
		return fmt.Errorf("publish result over a2a requires target agent")
	}
	metadata := target.Metadata()

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode a2a result payload: %w", err)
	}
	part := a2a.NewTextPart(string(encoded))
	part.MediaType = a2aResultPartMediaType
	msg := a2a.NewMessage(a2a.MessageRoleAgent, part)
	if messageID := firstString(
		payload["client_msg_id"],
		payload["clientMsgId"],
		payload["message_id"],
		payload["messageId"],
		payload["request_id"],
	); messageID != "" {
		msg.ID = messageID
	}
	if taskID := firstString(payload["a2a_task_id"], payload["hub_task_id"]); taskID != "" {
		msg.TaskID = a2a.TaskID(taskID)
	}
	if contextID := firstString(payload["a2a_context_id"], payload["context_id"]); contextID != "" {
		msg.ContextID = contextID
	}
	msg.Metadata = metadata

	endpoint := c.a2aEndpointURLForTarget(target)
	err = c.sendResultA2A(ctx, normalizedToken, endpoint, msg, metadata)
	if err == nil {
		return nil
	}
	if endpoint != c.a2aEndpointURL() {
		c.logf("hub.a2a status=warn action=target_endpoint_fallback err=%q", err)
		return c.sendResultA2A(ctx, normalizedToken, c.a2aEndpointURL(), msg, metadata)
	}
	return err
}

func (c APIClient) sendResultA2A(
	ctx context.Context,
	token string,
	endpoint string,
	msg *a2a.Message,
	metadata map[string]any,
) error {
	client, err := c.newA2AClient(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("create a2a client: %w", err)
	}

	callCtx := a2aAuthorizedContext(ctx, token)
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

func (c APIClient) verifyTokenA2A(ctx context.Context, token string) (bool, error) {
	normalizedToken, err := requireHubToken(token, "verify token over a2a")
	if err != nil {
		return false, err
	}
	client, err := c.newA2AClient(ctx, c.a2aEndpointURL())
	if err != nil {
		return false, fmt.Errorf("create a2a client: %w", err)
	}
	_, cardErr := client.GetExtendedAgentCard(
		a2aAuthorizedContext(ctx, normalizedToken),
		&a2a.GetExtendedAgentCardRequest{},
	)
	destroyErr := client.Destroy()
	if cardErr != nil {
		return false, errors.Join(fmt.Errorf("a2a GetExtendedAgentCard: %w", cardErr), destroyErr)
	}
	if destroyErr != nil {
		return false, fmt.Errorf("destroy a2a client: %w", destroyErr)
	}
	return true, nil
}

func (c APIClient) newA2AClient(ctx context.Context, endpoint string) (*a2aclient.Client, error) {
	return a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{
		a2a.NewAgentInterface(endpoint, a2a.TransportProtocolJSONRPC),
	}, a2aclient.WithJSONRPCTransport(c.a2aHTTPClient()))
}

func (c APIClient) a2aHTTPClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 35 * time.Second}
}

func a2aAuthorizedContext(ctx context.Context, token string) context.Context {
	return a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams{
		"Authorization": []string{"Bearer " + strings.TrimSpace(token)},
	})
}

func (c APIClient) a2aEndpointURL() string {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/") + "/a2a"
}

func (c APIClient) a2aEndpointURLForTarget(target a2aRouteTarget) string {
	base := c.a2aEndpointURL()
	if target.AgentUUID == "" || !looksLikeUUID(target.AgentUUID) {
		return base
	}
	return base + "/agents/" + url.PathEscape(target.AgentUUID)
}

type a2aRouteTarget struct {
	AgentUUID string
	AgentURI  string
	AgentID   string
}

func (t a2aRouteTarget) Metadata() map[string]any {
	metadata := map[string]any{}
	if t.AgentURI != "" {
		metadata["to_agent_uri"] = t.AgentURI
	} else if t.AgentUUID != "" {
		metadata["to_agent_uuid"] = t.AgentUUID
	} else if t.AgentID != "" {
		metadata["to_agent_id"] = t.AgentID
	}
	return metadata
}

func publishResultA2ARoutingMetadata(payload map[string]any) map[string]any {
	target, ok := publishResultA2ARouteTarget(payload)
	if !ok {
		return nil
	}
	return target.Metadata()
}

func publishResultA2ARouteTarget(payload map[string]any) (a2aRouteTarget, bool) {
	_, routed := publishResultRuntimeBody(payload)
	if !routed {
		return a2aRouteTarget{}, false
	}
	if toAgentURI := firstString(payload["to_agent_uri"]); toAgentURI != "" {
		return a2aRouteTarget{AgentURI: toAgentURI}, true
	} else if toAgentUUID := firstString(payload["to_agent_uuid"]); toAgentUUID != "" {
		return a2aRouteTarget{AgentUUID: toAgentUUID}, true
	} else if toAgentID := firstString(payload["to_agent_id"]); toAgentID != "" {
		return a2aRouteTarget{AgentID: toAgentID}, true
	} else if routeTarget := firstString(payload["to"], payload["reply_to"]); routeTarget != "" {
		if looksLikeAgentURI(routeTarget) {
			return a2aRouteTarget{AgentURI: routeTarget}, true
		} else if looksLikeUUID(routeTarget) {
			return a2aRouteTarget{AgentUUID: routeTarget}, true
		} else {
			return a2aRouteTarget{AgentID: routeTarget}, true
		}
	}
	return a2aRouteTarget{}, false
}

func looksLikeUUID(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				return false
			}
		}
	}
	return true
}

func publishResultRuntimeBody(payload map[string]any) (map[string]any, bool) {
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
	} else if toAgentID := firstString(payload["to_agent_id"]); toAgentID != "" {
		body["to_agent_id"] = toAgentID
		routed = true
	} else if routeTarget := firstString(payload["to"], payload["reply_to"]); routeTarget != "" {
		if looksLikeAgentURI(routeTarget) {
			body["to_agent_uri"] = routeTarget
		} else if looksLikeUUID(routeTarget) {
			body["to_agent_uuid"] = routeTarget
		} else {
			body["to_agent_id"] = routeTarget
		}
		routed = true
	}
	if clientMsgID := firstString(
		payload["client_msg_id"],
		payload["clientMsgId"],
		payload["message_id"],
		payload["messageId"],
		payload["request_id"],
	); clientMsgID != "" {
		body["client_msg_id"] = clientMsgID
	}
	return body, routed
}

func publishResultOpenClawBody(payload map[string]any) (map[string]any, bool) {
	return publishResultRuntimeBody(payload)
}
