package web

import "fmt"

func priorToolStateExcludingTrustedReplay(priorCallIDs, priorSeenCallDigests, trustedReplayCallDigests []string) ([]string, []string) {
	replayed := make(map[string]struct{}, len(trustedReplayCallDigests))
	for _, digest := range trustedReplayCallDigests {
		if validCheckpointDigest(digest) {
			replayed[digest] = struct{}{}
		}
	}
	if len(replayed) == 0 {
		return priorCallIDs, priorSeenCallDigests
	}
	filteredIDs := make([]string, 0, len(priorCallIDs))
	for _, id := range priorCallIDs {
		if _, ok := replayed[toolCallIDDigest(id)]; !ok {
			filteredIDs = append(filteredIDs, id)
		}
	}
	filteredDigests := make([]string, 0, len(priorSeenCallDigests))
	for _, digest := range priorSeenCallDigests {
		if _, ok := replayed[digest]; !ok {
			filteredDigests = append(filteredDigests, digest)
		}
	}
	return filteredIDs, filteredDigests
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
