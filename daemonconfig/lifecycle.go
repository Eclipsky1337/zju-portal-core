package daemonconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

type ApplyRequirement string

const (
	ApplyRequirementLive           ApplyRequirement = "live"
	ApplyRequirementSessionRestart ApplyRequirement = "session_restart"
	ApplyRequirementCoreRestart    ApplyRequirement = "core_restart"
)

type Change struct {
	Path     string           `json:"path"`
	Requires ApplyRequirement `json:"requires"`
}

type Snapshot struct {
	Revision       uint64   `json:"revision"`
	Configured     Config   `json:"configured"`
	Active         Config   `json:"active"`
	ActiveRevision uint64   `json:"active_revision"`
	Pending        []Change `json:"pending"`
}

type ApplyMode string

const ApplyModeRestartSession ApplyMode = "restart-session"

func Changes(active, configured Config) []Change {
	var paths []string
	collectChanges(reflect.ValueOf(active), reflect.ValueOf(configured), "", &paths)
	changes := make([]Change, 0, len(paths))
	for _, path := range paths {
		if path == "session.auto-start" {
			continue
		}
		changes = append(changes, Change{Path: path, Requires: requirementForPath(path)})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

func MergeJSON(config Config, patch []byte) (Config, error) {
	baseData, err := json.Marshal(config)
	if err != nil {
		return Config{}, fmt.Errorf("encode base config: %w", err)
	}
	var baseValue map[string]any
	if err := decodeJSONObject(baseData, &baseValue); err != nil {
		return Config{}, fmt.Errorf("decode base config: %w", err)
	}
	var patchValue map[string]any
	if err := decodeJSONObject(patch, &patchValue); err != nil {
		return Config{}, fmt.Errorf("decode config patch: %w", err)
	}
	mergeObject(baseValue, patchValue)
	mergedData, err := json.Marshal(baseValue)
	if err != nil {
		return Config{}, fmt.Errorf("encode merged config: %w", err)
	}
	var merged Config
	if err := json.Unmarshal(mergedData, &merged); err != nil {
		return Config{}, fmt.Errorf("decode merged config: %w", err)
	}
	if err := merged.Validate(); err != nil {
		return Config{}, err
	}
	return merged, nil
}

func requirementForPath(path string) ApplyRequirement {
	switch {
	case path == "routing.mode":
		return ApplyRequirementLive
	case strings.HasPrefix(path, "control."),
		strings.HasPrefix(path, "log."),
		strings.HasPrefix(path, "state."),
		strings.HasPrefix(path, "inbounds.tun."):
		return ApplyRequirementCoreRestart
	default:
		return ApplyRequirementSessionRestart
	}
}

func collectChanges(active, configured reflect.Value, path string, paths *[]string) {
	if active.Type() != configured.Type() {
		*paths = append(*paths, path)
		return
	}
	switch active.Kind() {
	case reflect.Struct:
		typeInfo := active.Type()
		for index := 0; index < active.NumField(); index++ {
			fieldInfo := typeInfo.Field(index)
			name := strings.Split(fieldInfo.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			fieldPath := name
			if path != "" {
				fieldPath = path + "." + name
			}
			collectChanges(active.Field(index), configured.Field(index), fieldPath, paths)
		}
	case reflect.Map, reflect.Slice:
		if !reflect.DeepEqual(active.Interface(), configured.Interface()) {
			*paths = append(*paths, path)
		}
	default:
		if !reflect.DeepEqual(active.Interface(), configured.Interface()) {
			*paths = append(*paths, path)
		}
	}
}

func decodeJSONObject(data []byte, target *map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if *target == nil {
		return fmt.Errorf("expected JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func mergeObject(base, patch map[string]any) {
	for key, patchValue := range patch {
		patchObject, patchIsObject := patchValue.(map[string]any)
		baseObject, baseIsObject := base[key].(map[string]any)
		if patchIsObject && baseIsObject {
			mergeObject(baseObject, patchObject)
			continue
		}
		base[key] = patchValue
	}
}
