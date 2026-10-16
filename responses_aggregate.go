package main

import (
	"math"
	"sort"
	"strings"
)

// Responses terminals may omit output. Keep the actual streamed items, and
// reconcile done/full snapshots as replacements rather than additional deltas.
// Output indexes belong to events (or terminal array positions), not items;
// call_id is data, never an item identity or a substitute for item.id.
type responsesAggregate struct {
	byIndex map[int]*responsesOutput
	byID    map[string]*responsesOutput
	items   []*responsesOutput
}

type responsesOutput struct {
	value            map[string]any
	index            int
	indexed          bool
	id               string
	identified       bool
	content, summary map[int]map[string]any
}

func newResponsesAggregate() *responsesAggregate {
	return &responsesAggregate{byIndex: map[int]*responsesOutput{}, byID: map[string]*responsesOutput{}}
}

func responsesIndex(o map[string]any, key string) (int, bool, error) {
	v, exists := o[key]
	if !exists {
		return 0, false, nil
	}
	n, ok := v.(float64)
	// JSON numbers are float64 here. Bound them before any int conversion;
	// indexes are sparse map keys, never allocation sizes.
	if !ok || n < 0 || n > float64(maxResponseBytes) || math.Trunc(n) != n {
		return 0, false, fail(502, "Responses event has an invalid "+key)
	}
	return int(n), true, nil
}

func responsesID(o map[string]any, key string) (string, bool, error) {
	v, exists := o[key]
	if !exists {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, fail(502, "Responses event has an invalid item identity")
	}
	return s, true, nil
}

func (a *responsesAggregate) item(event, snapshot map[string]any) (*responsesOutput, error) {
	index, indexed, err := responsesIndex(event, "output_index")
	if err != nil {
		return nil, err
	}
	id, identified, err := responsesID(event, "item_id")
	if err != nil {
		return nil, err
	}
	if itemID, present, err := responsesID(snapshot, "id"); err != nil {
		return nil, err
	} else if present {
		if identified && id != itemID {
			return nil, fail(502, "Responses event has conflicting item identities")
		}
		id, identified = itemID, true
	}
	if !indexed && !identified {
		return nil, fail(502, "Responses output event has no item identity or output_index")
	}
	var item *responsesOutput
	if indexed {
		item = a.byIndex[index]
	}
	if identified {
		if found := a.byID[id]; found != nil {
			if item != nil && item != found {
				return nil, fail(502, "Responses output index and item identity disagree")
			}
			item = found
		}
	}
	if item == nil {
		item = &responsesOutput{value: map[string]any{}, content: map[int]map[string]any{}, summary: map[int]map[string]any{}}
		a.items = append(a.items, item)
	}
	if indexed {
		if item.indexed && item.index != index {
			return nil, fail(502, "Responses item changed output_index")
		}
		item.index, item.indexed = index, true
		a.byIndex[index] = item
	}
	if identified {
		if item.identified && item.id != id {
			return nil, fail(502, "Responses output index changed item identity")
		}
		item.id, item.identified = id, true
		item.value["id"] = id
		a.byID[id] = item
	}
	return item, nil
}

func responsesMergeFields(dst, src map[string]any) error {
	for _, key := range []string{"id", "call_id", "type"} {
		before, present := dst[key]
		after, supplied := src[key]
		if supplied {
			if _, ok := after.(string); !ok {
				return fail(502, "Responses output has an invalid "+key)
			}
		}
		if present && supplied && before != after {
			return fail(502, "Responses output has conflicting "+key)
		}
	}
	for key, value := range src {
		dst[key] = resolverClone(value)
	}
	return nil
}

func (item *responsesOutput) part(section string, index int, snapshot map[string]any) (map[string]any, error) {
	parts := item.content
	if section == "summary" {
		parts = item.summary
	}
	part := parts[index]
	if part == nil {
		part = map[string]any{}
		parts[index] = part
	}
	if err := responsesMergeFields(part, snapshot); err != nil {
		return nil, err
	}
	return part, nil
}

func (item *responsesOutput) merge(snapshot map[string]any) error {
	fields := map[string]any{}
	for key, value := range snapshot {
		if key != "content" && key != "summary" {
			fields[key] = value
			continue
		}
		parts, ok := value.([]any)
		if !ok {
			return fail(502, "Responses item has an invalid "+key)
		}
		// Preserve the explicit empty array without erasing already streamed parts.
		item.value[key] = []any{}
		for index, value := range parts {
			part := object(value)
			if part == nil {
				return fail(502, "Responses item has an invalid content part")
			}
			if _, err := item.part(key, index, part); err != nil {
				return err
			}
		}
	}
	return responsesMergeFields(item.value, fields)
}

func (a *responsesAggregate) response(response, result map[string]any) error {
	for key, value := range response {
		if key != "output" {
			if key == "usage" && object(value) != nil {
				result[key] = resolverMerge(object(result[key]), object(value))
				continue
			}
			result[key] = resolverClone(value)
			continue
		}
		items, ok := value.([]any)
		if !ok {
			return fail(502, "Responses response has an invalid output")
		}
		for index, value := range items {
			snapshot := object(value)
			if snapshot == nil {
				return fail(502, "Responses response has an invalid output item")
			}
			item, err := a.item(map[string]any{"output_index": float64(index)}, snapshot)
			if err != nil {
				return err
			}
			if err := item.merge(snapshot); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *responsesAggregate) add(event, result map[string]any) error {
	typ, _ := event["type"].(string)
	switch typ {
	case "response.created", "response.in_progress", "response.completed", "response.incomplete":
		response := object(event["response"])
		if response == nil {
			if typ == "response.completed" || typ == "response.incomplete" {
				return fail(502, "Responses terminal event has no response")
			}
			return nil
		}
		if err := a.response(response, result); err != nil {
			return err
		}
		if typ == "response.completed" || typ == "response.incomplete" {
			result["status"] = strings.TrimPrefix(typ, "response.")
		}
		return nil
	case "response.output_item.added", "response.output_item.done":
		if event["item"] == nil {
			return nil
		} // Nullable in the official stream schema.
		snapshot := object(event["item"])
		if snapshot == nil {
			return fail(502, "Responses event has an invalid output item")
		}
		item, err := a.item(event, snapshot)
		if err != nil {
			return err
		}
		return item.merge(snapshot)
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		item, err := a.item(event, nil)
		if err != nil {
			return err
		}
		if err := responsesMergeFields(item.value, map[string]any{"type": "function_call"}); err != nil {
			return err
		}
		if typ == "response.function_call_arguments.delta" {
			return responsesText(item.value, "arguments", event, "delta", true)
		}
		return responsesText(item.value, "arguments", event, "arguments", false)
	}

	var section, indexKey, itemType, partType, field, source string
	delta := strings.HasSuffix(typ, ".delta")
	switch typ {
	case "response.content_part.added", "response.content_part.done":
		section, indexKey = "content", "content_index"
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		section, indexKey, itemType = "summary", "summary_index", "reasoning"
	case "response.output_text.delta", "response.output_text.done":
		section, indexKey, itemType, partType, field, source = "content", "content_index", "message", "output_text", "text", "text"
	case "response.refusal.delta", "response.refusal.done":
		section, indexKey, itemType, partType, field, source = "content", "content_index", "message", "refusal", "refusal", "refusal"
	case "response.reasoning.delta", "response.reasoning.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		section, indexKey, itemType, partType, field, source = "summary", "summary_index", "reasoning", "summary_text", "text", "text"
	case "response.reasoning_text.delta", "response.reasoning_text.done":
		section, indexKey, itemType, partType, field, source = "content", "content_index", "reasoning", "reasoning_text", "text", "text"
	default:
		return nil
	}
	item, err := a.item(event, nil)
	if err != nil {
		return err
	}
	if itemType != "" {
		if err := responsesMergeFields(item.value, map[string]any{"type": itemType}); err != nil {
			return err
		}
		if itemType == "message" {
			if _, exists := item.value["role"]; !exists {
				item.value["role"] = "assistant"
			}
		}
	}
	index, _, err := responsesIndex(event, indexKey)
	if err != nil {
		return err
	}
	if field == "" {
		snapshot := object(event["part"])
		if snapshot == nil {
			return fail(502, "Responses event has an invalid content part")
		}
		if section == "content" {
			inferred := map[string]any{}
			switch snapshot["type"] {
			case "output_text", "refusal":
				inferred["type"] = "message"
				if _, present := item.value["role"]; !present {
					inferred["role"] = "assistant"
				}
			case "reasoning_text":
				inferred["type"] = "reasoning"
			}
			if err := responsesMergeFields(item.value, inferred); err != nil {
				return err
			}
		}
		_, err = item.part(section, index, snapshot)
		return err
	}
	part, err := item.part(section, index, map[string]any{"type": partType})
	if err != nil {
		return err
	}
	if delta {
		source = "delta"
	}
	return responsesText(part, field, event, source, delta)
}

func responsesText(dst map[string]any, key string, event map[string]any, source string, delta bool) error {
	value, ok := event[source].(string)
	if !ok {
		return fail(502, "Responses event has invalid "+source)
	}
	if delta {
		appendText(dst, key, value)
	} else {
		dst[key] = value
	}
	return nil
}

func (a *responsesAggregate) output() []any {
	items := append([]*responsesOutput(nil), a.items...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].indexed != items[j].indexed {
			return items[i].indexed
		}
		return items[i].indexed && items[i].index < items[j].index
	})
	output := make([]any, 0, len(items))
	for _, item := range items {
		for section, parts := range map[string]map[int]map[string]any{"content": item.content, "summary": item.summary} {
			if len(parts) == 0 {
				continue
			}
			values := make([]any, 0, len(parts))
			for _, index := range sortedIndexes(parts) {
				values = append(values, parts[index])
			}
			item.value[section] = values
		}
		output = append(output, item.value)
	}
	return output
}
