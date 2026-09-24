package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var configWriteMu sync.Mutex

func updateConfigColor(path, name, color string) error {
	return updateConfigField(path, "processes", name, "color", color)
}

func updateConfigField(path, mapping, name, key, value string) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("config is not a YAML mapping")
	}
	root := doc.Content[0]
	entries := mappingValue(root, mapping)
	if entries == nil || entries.Kind != yaml.MappingNode {
		return fmt.Errorf("config has no %s mapping", mapping)
	}
	var entryKey, entryValue *yaml.Node
	for i := 0; i+1 < len(entries.Content); i += 2 {
		if entries.Content[i].Value == name {
			entryKey, entryValue = entries.Content[i], entries.Content[i+1]
			break
		}
	}
	if entryKey == nil {
		return fmt.Errorf("%s entry %q not found in %s", mapping, name, path)
	}
	if entryValue.Kind != yaml.MappingNode {
		return fmt.Errorf("%s entry %q is not a mapping", mapping, name)
	}

	out, err := spliceConfigField(string(data), &doc, mapping, entryKey, entryValue, key, value)
	if err == nil {
		err = verifyConfigField(out, mapping, name, key, value)
	}
	if err != nil {
		out, err = encodeConfigField(root, entryValue, key, value)
		if err != nil {
			return err
		}
		if err := verifyConfigField(out, mapping, name, key, value); err != nil {
			return err
		}
	}
	return writeConfigFile(path, out)
}

func updateConfigOrder(path, mapping string, names []string) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("config is not a YAML mapping")
	}
	root := doc.Content[0]
	entries := mappingValue(root, mapping)
	if entries == nil || entries.Kind != yaml.MappingNode {
		return fmt.Errorf("config has no %s mapping", mapping)
	}
	current := make([]string, 0, len(entries.Content)/2)
	for i := 0; i+1 < len(entries.Content); i += 2 {
		current = append(current, entries.Content[i].Value)
	}
	if !samePermutation(current, names) {
		return fmt.Errorf("order %v is not a permutation of %s %v", names, mapping, current)
	}

	out, err := spliceConfigOrder(string(data), &doc, root, mapping, entries, names)
	if err == nil {
		err = verifyOrder(out, mapping, names)
	}
	if err != nil {
		out, err = encodeConfigOrder(root, entries, names)
		if err != nil {
			return err
		}
		if err := verifyOrder(out, mapping, names); err != nil {
			return err
		}
	}
	return writeConfigFile(path, out)
}

func spliceConfigOrder(text string, doc, root *yaml.Node, mapping string, entries *yaml.Node, names []string) (string, error) {
	if entries.Style&yaml.FlowStyle != 0 {
		return "", fmt.Errorf("flow-style %s mapping", mapping)
	}
	lines := strings.Split(text, "\n")

	mappingKey := findKeyNode(root, mapping)
	if mappingKey == nil {
		return "", fmt.Errorf("%s key not found", mapping)
	}
	if len(entries.Content) == 0 {
		return text, nil
	}

	type block struct {
		name       string
		start, end int
	}
	blocks := make([]block, 0, len(names))
	for i := 0; i+1 < len(entries.Content); i += 2 {
		key, val := entries.Content[i], entries.Content[i+1]
		if val.Style&yaml.FlowStyle != 0 || val.Line == key.Line {
			return "", fmt.Errorf("inline %s entry", mapping)
		}
		blocks = append(blocks, block{name: key.Value, start: key.Line - 1})
	}
	for i := 1; i < len(blocks); i++ {
		if blocks[i].start <= blocks[i-1].start {
			return "", fmt.Errorf("unexpected %s line layout", mapping)
		}
	}

	sectionEnd := len(lines)
	last := blocks[len(blocks)-1].start
	walkKeys(doc, func(k *yaml.Node) {
		if k.Line-1 > last && k.Column <= mappingKey.Column && k.Line-1 < sectionEnd {
			sectionEnd = k.Line - 1
		}
	})

	for i := range blocks {
		floor := mappingKey.Line
		if i > 0 {
			floor = blocks[i-1].start
		}
		for blocks[i].start > floor && strings.HasPrefix(strings.TrimSpace(lines[blocks[i].start-1]), "#") {
			blocks[i].start--
		}
	}
	for i := range blocks {
		if i+1 < len(blocks) {
			blocks[i].end = blocks[i+1].start
		} else {
			blocks[i].end = sectionEnd
		}
	}

	blankSep := false
	contents := make(map[string][]string, len(blocks))
	for _, b := range blocks {
		chunk := lines[b.start:b.end]
		for len(chunk) > 0 && strings.TrimSpace(chunk[len(chunk)-1]) == "" {
			chunk = chunk[:len(chunk)-1]
			blankSep = true
		}
		contents[b.name] = chunk
	}

	var rebuilt []string
	rebuilt = append(rebuilt, lines[:blocks[0].start]...)
	for i, name := range names {
		if i > 0 && blankSep {
			rebuilt = append(rebuilt, "")
		}
		rebuilt = append(rebuilt, contents[name]...)
	}
	if sectionEnd < len(lines) {
		if blankSep {
			rebuilt = append(rebuilt, "")
		}
		rebuilt = append(rebuilt, lines[sectionEnd:]...)
	} else if strings.HasSuffix(text, "\n") {
		rebuilt = append(rebuilt, "")
	}
	return strings.Join(rebuilt, "\n"), nil
}

func verifyOrder(text, mapping string, names []string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return errors.New("spliced config is empty")
	}
	entries := mappingValue(doc.Content[0], mapping)
	if entries == nil || entries.Kind != yaml.MappingNode {
		return fmt.Errorf("spliced config lost the %s mapping", mapping)
	}
	got := make([]string, 0, len(entries.Content)/2)
	for i := 0; i+1 < len(entries.Content); i += 2 {
		got = append(got, entries.Content[i].Value)
	}
	if len(got) != len(names) {
		return fmt.Errorf("spliced config changed the %s count", mapping)
	}
	for i := range got {
		if got[i] != names[i] {
			return fmt.Errorf("spliced config does not match the requested %s order", mapping)
		}
	}
	return nil
}

func encodeConfigOrder(root, entries *yaml.Node, names []string) (string, error) {
	pairs := make(map[string][2]*yaml.Node, len(names))
	for i := 0; i+1 < len(entries.Content); i += 2 {
		pairs[entries.Content[i].Value] = [2]*yaml.Node{entries.Content[i], entries.Content[i+1]}
	}
	reordered := make([]*yaml.Node, 0, len(entries.Content))
	for _, name := range names {
		pair := pairs[name]
		reordered = append(reordered, pair[0], pair[1])
	}
	entries.Content = reordered
	return encodeRoot(root)
}

// updateConfigUIFlag rewrites (or creates) a boolean field under `ui:`,
// preserving the rest of the file.
func updateConfigUIFlag(path, key string, value bool) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("config is not a YAML mapping")
	}
	root := doc.Content[0]

	out, err := spliceUIFlag(string(data), &doc, root, key, value)
	if err == nil {
		var check map[string]any
		if yaml.Unmarshal([]byte(out), &check) != nil {
			err = errors.New("spliced config does not parse")
		}
	}
	if err != nil {
		out, err = encodeUIFlag(root, key, value)
		if err != nil {
			return err
		}
	}
	return writeConfigFile(path, out)
}

func spliceUIFlag(text string, doc, root *yaml.Node, key string, value bool) (string, error) {
	lines := strings.Split(text, "\n")
	val := "false"
	if value {
		val = "true"
	}

	ui := mappingValue(root, "ui")
	uiKey := findKeyNode(root, "ui")
	if ui != nil && ui.Kind == yaml.MappingNode && uiKey != nil {
		if ui.Style&yaml.FlowStyle != 0 || len(ui.Content) == 0 {
			return "", errors.New("unsupported ui mapping layout")
		}
		for i := 0; i+1 < len(ui.Content); i += 2 {
			if ui.Content[i].Value != key {
				continue
			}
			v := ui.Content[i+1]
			if v.Line != ui.Content[i].Line {
				return "", errors.New("unexpected ui field layout")
			}
			idx := v.Line - 1
			old := lines[idx]
			indent := old[:len(old)-len(strings.TrimLeft(old, " \t"))]
			replaced := indent + key + ": " + val
			if v.LineComment != "" {
				replaced += "  " + v.LineComment
			}
			lines[idx] = replaced
			return strings.Join(lines, "\n"), nil
		}
		// Key missing: insert after the last ui field.
		boundary := len(lines)
		walkKeys(doc, func(k *yaml.Node) {
			if k.Line > uiKey.Line && k.Column <= uiKey.Column && k.Line-1 < boundary {
				boundary = k.Line - 1
			}
		})
		insert := boundary
		for insert > uiKey.Line {
			prev := strings.TrimSpace(lines[insert-1])
			if prev == "" || strings.HasPrefix(prev, "#") {
				insert--
			} else {
				break
			}
		}
		indent := strings.Repeat(" ", ui.Content[0].Column-1)
		newLine := indent + key + ": " + val
		lines = append(lines[:insert], append([]string{newLine}, lines[insert:]...)...)
		return strings.Join(lines, "\n"), nil
	}

	// No ui section: create one right above the processes block (including
	// its head comments).
	procKey := findKeyNode(root, "processes")
	if procKey == nil {
		return "", errors.New("processes key not found")
	}
	insert := procKey.Line - 1
	for insert > 0 && strings.HasPrefix(strings.TrimSpace(lines[insert-1]), "#") {
		insert--
	}
	section := []string{"ui:", "  " + key + ": " + val, ""}
	lines = append(lines[:insert], append(section, lines[insert:]...)...)
	return strings.Join(lines, "\n"), nil
}

func encodeUIFlag(root *yaml.Node, key string, value bool) (string, error) {
	val := "false"
	if value {
		val = "true"
	}
	scalar := func(v, tag string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: v}
	}
	ui := mappingValue(root, "ui")
	if ui == nil || ui.Kind != yaml.MappingNode {
		ui = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, scalar("ui", "!!str"), ui)
	}
	found := false
	for i := 0; i+1 < len(ui.Content); i += 2 {
		if ui.Content[i].Value == key {
			ui.Content[i+1].SetString(val)
			ui.Content[i+1].Tag = "!!bool"
			ui.Content[i+1].Style = 0
			found = true
			break
		}
	}
	if !found {
		ui.Content = append(ui.Content, scalar(key, "!!str"), scalar(val, "!!bool"))
	}
	return encodeRoot(root)
}

func encodeRoot(root *yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeConfigFile(path, out string) error {
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, []byte(out), mode)
}

func findKeyNode(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i]
		}
	}
	return nil
}

func samePermutation(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}

func spliceConfigField(text string, doc *yaml.Node, mapping string, entryKey, entryValue *yaml.Node, key, value string) (string, error) {
	if entryValue.Style&yaml.FlowStyle != 0 {
		return "", fmt.Errorf("flow-style %s entry", mapping)
	}
	lines := strings.Split(text, "\n")

	var fieldKey, fieldValue *yaml.Node
	for i := 0; i+1 < len(entryValue.Content); i += 2 {
		if entryValue.Content[i].Value == key {
			fieldKey, fieldValue = entryValue.Content[i], entryValue.Content[i+1]
			break
		}
	}

	if fieldKey != nil {
		idx := fieldKey.Line - 1
		if idx < 0 || idx >= len(lines) || fieldValue.Line != fieldKey.Line {
			return "", fmt.Errorf("unexpected %s field line layout", key)
		}
		if value == "" {
			lines = append(lines[:idx], lines[idx+1:]...)
		} else {
			scalar, err := encodeStringScalar(value)
			if err != nil {
				return "", err
			}
			old := lines[idx]
			indent := old[:len(old)-len(strings.TrimLeft(old, " \t"))]
			replaced := indent + key + ": " + scalar
			if fieldValue.LineComment != "" {
				replaced += "  " + fieldValue.LineComment
			}
			lines[idx] = replaced
		}
		return strings.Join(lines, "\n"), nil
	}

	if value == "" {
		return strings.Join(lines, "\n"), nil
	}

	boundary := len(lines)
	walkKeys(doc, func(k *yaml.Node) {
		if k.Line > entryKey.Line && k.Column <= entryKey.Column && k.Line-1 < boundary {
			boundary = k.Line - 1
		}
	})
	insert := boundary
	for insert > entryKey.Line {
		prev := strings.TrimSpace(lines[insert-1])
		if prev == "" || strings.HasPrefix(prev, "#") {
			insert--
		} else {
			break
		}
	}
	if len(entryValue.Content) == 0 || entryValue.Content[0].Column < 1 {
		return "", errors.New("cannot determine field indent")
	}
	scalar, err := encodeStringScalar(value)
	if err != nil {
		return "", err
	}
	indent := strings.Repeat(" ", entryValue.Content[0].Column-1)
	newLine := indent + key + ": " + scalar
	lines = append(lines[:insert], append([]string{newLine}, lines[insert:]...)...)
	return strings.Join(lines, "\n"), nil
}

func verifyConfigField(text, mapping, name, key, value string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("spliced config is not a YAML mapping")
	}
	entries := mappingValue(doc.Content[0], mapping)
	if entries == nil || entries.Kind != yaml.MappingNode {
		return fmt.Errorf("spliced config lost the %s mapping", mapping)
	}
	entry := mappingValue(entries, name)
	if entry == nil || entry.Kind != yaml.MappingNode {
		return fmt.Errorf("spliced config lost the %s entry %q", mapping, name)
	}
	field := mappingValue(entry, key)
	if value == "" {
		if field != nil {
			return fmt.Errorf("spliced config retained the %s field", key)
		}
		return nil
	}
	if field == nil || field.Kind != yaml.ScalarNode || field.Tag != "!!str" || field.Value != value {
		return fmt.Errorf("spliced config does not match the requested %s value", key)
	}
	return nil
}

func encodeConfigField(root, entry *yaml.Node, key, value string) (string, error) {
	for i := 0; i+1 < len(entry.Content); i += 2 {
		if entry.Content[i].Value != key {
			continue
		}
		if value == "" {
			entry.Content = append(entry.Content[:i], entry.Content[i+2:]...)
		} else {
			previous := entry.Content[i+1]
			entry.Content[i+1] = stringValueNode(value, previous)
		}
		return encodeRoot(root)
	}
	if value != "" {
		entry.Content = append(entry.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			stringValueNode(value, nil),
		)
	}
	return encodeRoot(root)
}

func stringValueNode(value string, previous *yaml.Node) *yaml.Node {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle}
	if previous != nil {
		node.HeadComment = previous.HeadComment
		node.LineComment = previous.LineComment
		node.FootComment = previous.FootComment
	}
	return node
}

func encodeStringScalar(value string) (string, error) {
	data, err := yaml.Marshal(stringValueNode(value, nil))
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}

// walkKeys visits every mapping key node in the document.
func walkKeys(n *yaml.Node, fn func(k *yaml.Node)) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			fn(n.Content[i])
			walkKeys(n.Content[i+1], fn)
		}
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			walkKeys(c, fn)
		}
	}
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
