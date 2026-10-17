package catalog

import (
	"bytes"
	"encoding/json"
)

// ResolvePublicRoutes exposes only the OpenCode catalog's explicitly zero-cost
// models. The official v1.19.3 client selects cost.input == 0 and uses the
// literal public API key. We additionally require explicit zero output cost
// and zero for every listed cost component: missing prices are not free.
// Catalog eligibility is metadata, not a guarantee of upstream authorization.
func ResolvePublicRoutes(data []byte) (map[string]Route, error) {
	if _, err := ParseCatalog(data); err != nil {
		return nil, err
	}
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, err
	}
	var provider map[string]json.RawMessage
	if raw, ok := providers["opencode"]; ok {
		if err := json.Unmarshal(raw, &provider); err != nil {
			return nil, err
		}
	}
	var models map[string]json.RawMessage
	if raw, ok := provider["models"]; ok {
		if err := json.Unmarshal(raw, &models); err != nil {
			return nil, err
		}
	}
	// Multiple catalog records claiming one canonical model ID are ambiguous.
	// Reject every such record before filtering prices (including paid records).
	idCounts := map[string]int{}
	for key, raw := range models {
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil {
			return nil, err
		}
		if identity.ID == "" {
			identity.ID = key
		}
		idCounts[identity.ID]++
	}
	freeIDs := map[string]bool{}
	for id, raw := range models {
		var model struct {
			Cost map[string]json.RawMessage `json:"cost"`
			ID   string                     `json:"id"`
		}
		if json.Unmarshal(raw, &model) != nil || !explicitlyFree(model.Cost) {
			delete(models, id)
		} else {
			if model.ID == "" {
				model.ID = id
			}
			if idCounts[model.ID] != 1 {
				delete(models, id)
				continue
			}
			freeIDs[model.ID] = true
		}
	}
	filtered := map[string]any{}
	if provider != nil {
		provider["models"], _ = json.Marshal(models)
		filtered["opencode"] = provider
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	routes, err := ResolveRoutes([]byte(`{"providers":{}}`), data)
	if err != nil {
		return nil, err
	}
	for alias, route := range routes {
		if !freeIDs[route.ModelID] {
			delete(routes, alias)
			continue
		}
		if model, present := route.Body["model"]; present && model != route.ModelID {
			delete(routes, alias)
		}
	}
	return routes, nil
}

func explicitlyFree(cost map[string]json.RawMessage) bool {
	if _, ok := cost["input"]; !ok {
		return false
	}
	if _, ok := cost["output"]; !ok {
		return false
	}
	for _, raw := range cost {
		var number any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&number) != nil {
			return false
		}
		n, ok := number.(json.Number)
		if !ok {
			return false
		}
		// Check the mantissa exactly: float underflow must never make a fee free.
		for _, digit := range n.String() {
			if digit == 'e' || digit == 'E' {
				break
			}
			if digit >= '1' && digit <= '9' {
				return false
			}
		}
	}
	return true
}
