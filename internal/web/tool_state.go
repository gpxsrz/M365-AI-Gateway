package web

import "fmt"

// validateToolConversation enforces the OpenAI tool protocol without making
// assumptions about what a tool does. A later user turn may explicitly abandon
// an interrupted client-side batch, but call IDs remain unique and a trailing
// batch still requires exactly one matching result per call.
func validateToolConversation(messages []oaiMsg) error {
	return validateToolConversationWithPrior(messages, nil)
}

func validateToolConversationWithPrior(messages []oaiMsg, priorCallIDs []string, priorSeenCallIDs ...[]string) error {
	var seenDigests []string
	if len(priorSeenCallIDs) > 0 {
		seenDigests = make([]string, 0, len(priorSeenCallIDs[0]))
		for _, id := range priorSeenCallIDs[0] {
			seenDigests = append(seenDigests, toolCallIDDigest(id))
		}
	}
	return validateToolConversationWithPriorDigests(messages, priorCallIDs, seenDigests)
}

func validateToolConversationWithPriorDigests(messages []oaiMsg, priorCallIDs, priorSeenCallDigests []string) error {
	pending := map[string]bool{}
	seen := map[string]bool{}
	for _, digest := range priorSeenCallDigests {
		if !validCheckpointDigest(digest) || seen[digest] {
			return fmt.Errorf("invalid checkpoint tool call digest")
		}
		seen[digest] = true
	}
	for _, id := range priorCallIDs {
		digest := toolCallIDDigest(id)
		if id == "" || seen[digest] {
			return fmt.Errorf("invalid checkpoint tool call id: %s", id)
		}
		pending[id] = true
		seen[digest] = true
	}
	for i, m := range messages {
		switch m.Role {
		case "assistant":
			if len(pending) > 0 {
				return fmt.Errorf("tool results missing before assistant message at index %d", i)
			}
			for _, call := range m.ToolCalls {
				id, _ := call["id"].(string)
				if id == "" {
					return fmt.Errorf("assistant tool call missing id at index %d", i)
				}
				digest := toolCallIDDigest(id)
				if seen[digest] {
					return fmt.Errorf("duplicate tool call id: %s", id)
				}
				pending[id] = true
				seen[digest] = true
			}
		case "tool":
			if m.ToolCallID == "" {
				return fmt.Errorf("tool_call_id required at index %d", i)
			}
			if !pending[m.ToolCallID] {
				return fmt.Errorf("unexpected tool result: %s", m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		case "user":
			// A new user turn closes an interrupted client-side tool batch. The
			// evidence ledger still retains those calls as pending/unknown so the
			// sidecar cannot treat them as success or automatically repeat them.
			clear(pending)
		}
	}
	if len(pending) > 0 {
		for id := range pending {
			return fmt.Errorf("missing tool result for tool_call_id: %s", id)
		}
	}
	return nil
}
