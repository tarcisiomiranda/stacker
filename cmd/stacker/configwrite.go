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

// configWriteMu serializes config rewrites; the TUI (`c`) and the web color
// selector can both write, potentially at the same time.
var configWriteMu sync.Mutex

// updateConfigColor rewrites the `color` field of one process in the YAML
// config. It edits the raw text at the line located through the yaml.Node
// tree, so comments, blank lines, key order, and formatting all survive.
// An empty color removes the field.
func updateConfigColor(path, name, color string) error {
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
	processes := mappingValue(root, "processes")
	if processes == nil || processes.Kind != yaml.MappingNode {
		return errors.New("config has no processes mapping")
	}
	var procKey, procVal *yaml.Node
	for i := 0; i+1 < len(processes.Content); i += 2 {
		if processes.Content[i].Value == name {
			procKey, procVal = processes.Content[i], processes.Content[i+1]
			break
		}
	}
	if procKey == nil {
		return fmt.Errorf("process %q not found in %s", name, path)
	}
	if procVal.Kind != yaml.MappingNode {
		return fmt.Errorf("process %q is not a mapping", name)
	}

	out, err := spliceColorLine(string(data), &doc, procKey, procVal, color)
	if err == nil {
		// Guard against a bad splice before touching the file.
		var check map[string]any
		if yaml.Unmarshal([]byte(out), &check) != nil {
			err = errors.New("spliced config does not parse")
		}
	}
	if err != nil {
		// Fallback: re-encode the whole tree. Keeps comments but may drop
		// blank lines; only used for layouts the splicer does not handle.
		out, err = encodeConfigColor(root, procVal, color)
		if err != nil {
			return err
		}
	}

	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, []byte(out), mode)
}

// updateConfigOrder rewrites the YAML so the processes mapping follows the
// given name order. It moves whole text blocks (including each process's
// preceding comment lines), so formatting survives; falls back to a tree
// re-encode for layouts the splicer cannot handle.
func updateConfigOrder(path string, names []string) error {
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
	processes := mappingValue(root, "processes")
	if processes == nil || processes.Kind != yaml.MappingNode {
		return errors.New("config has no processes mapping")
	}
	current := make([]string, 0, len(processes.Content)/2)
	for i := 0; i+1 < len(processes.Content); i += 2 {
		current = append(current, processes.Content[i].Value)
	}
	if !samePermutation(current, names) {
		return fmt.Errorf("order %v is not a permutation of processes %v", names, current)
	}

	out, err := spliceProcessOrder(string(data), &doc, root, processes, names)
	if err == nil {
		if verr := verifyOrder(out, names); verr != nil {
			err = verr
		}
	}
	if err != nil {
		out, err = encodeProcessOrder(root, processes, names)
		if err != nil {
			return err
		}
	}
	return writeConfigFile(path, out)
}

// spliceProcessOrder moves the text block of each process (its key line, the
// nested lines under it, and comment lines directly above it) into the
// requested order.
func spliceProcessOrder(text string, doc, root, processes *yaml.Node, names []string) (string, error) {
	if processes.Style&yaml.FlowStyle != 0 {
		return "", errors.New("flow-style processes mapping")
	}
	lines := strings.Split(text, "\n")

	procKey := findKeyNode(root, "processes")
	if procKey == nil {
		return "", errors.New("processes key not found")
	}

	type block struct {
		name       string
		start, end int // 0-based, end exclusive
	}
	blocks := make([]block, 0, len(names))
	for i := 0; i+1 < len(processes.Content); i += 2 {
		key, val := processes.Content[i], processes.Content[i+1]
		if val.Style&yaml.FlowStyle != 0 || val.Line == key.Line {
			return "", errors.New("inline process mapping")
		}
		blocks = append(blocks, block{name: key.Value, start: key.Line - 1})
	}
	for i := 1; i < len(blocks); i++ {
		if blocks[i].start <= blocks[i-1].start {
			return "", errors.New("unexpected process line layout")
		}
	}

	// Section end: the next key at the processes indent or shallower.
	sectionEnd := len(lines)
	last := blocks[len(blocks)-1].start
	walkKeys(doc, func(k *yaml.Node) {
		if k.Line-1 > last && k.Column <= procKey.Column && k.Line-1 < sectionEnd {
			sectionEnd = k.Line - 1
		}
	})

	// Pull comment lines directly above each key into its block.
	for i := range blocks {
		floor := procKey.Line // first line after the processes: key itself
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

	// Normalize: strip trailing blank lines per block, remember whether the
	// original used blank separators between blocks.
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

// verifyOrder checks the spliced YAML parses and lists processes as expected.
func verifyOrder(text string, names []string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return errors.New("spliced config is empty")
	}
	processes := mappingValue(doc.Content[0], "processes")
	if processes == nil {
		return errors.New("spliced config lost the processes mapping")
	}
	got := make([]string, 0, len(processes.Content)/2)
	for i := 0; i+1 < len(processes.Content); i += 2 {
		got = append(got, processes.Content[i].Value)
	}
	if len(got) != len(names) {
		return errors.New("spliced config changed the process count")
	}
	for i := range got {
		if got[i] != names[i] {
			return errors.New("spliced config does not match the requested order")
		}
	}
	return nil
}

func encodeProcessOrder(root, processes *yaml.Node, names []string) (string, error) {
	pairs := make(map[string][2]*yaml.Node, len(names))
	for i := 0; i+1 < len(processes.Content); i += 2 {
		pairs[processes.Content[i].Value] = [2]*yaml.Node{processes.Content[i], processes.Content[i+1]}
	}
	reordered := make([]*yaml.Node, 0, len(processes.Content))
	for _, name := range names {
		pair := pairs[name]
		reordered = append(reordered, pair[0], pair[1])
	}
	processes.Content = reordered
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

// spliceColorLine performs the line-level edit: replace or delete the existing
// `color:` line, or insert one at the end of the process block.
func spliceColorLine(text string, doc, procKey, procVal *yaml.Node, color string) (string, error) {
	if procVal.Style&yaml.FlowStyle != 0 {
		return "", errors.New("flow-style process mapping")
	}
	lines := strings.Split(text, "\n")

	var colorKey, colorVal *yaml.Node
	for i := 0; i+1 < len(procVal.Content); i += 2 {
		if procVal.Content[i].Value == "color" {
			colorKey, colorVal = procVal.Content[i], procVal.Content[i+1]
			break
		}
	}

	if colorKey != nil {
		idx := colorKey.Line - 1
		if idx < 0 || idx >= len(lines) || colorVal.Line != colorKey.Line {
			return "", errors.New("unexpected color line layout")
		}
		if color == "" {
			lines = append(lines[:idx], lines[idx+1:]...)
		} else {
			old := lines[idx]
			indent := old[:len(old)-len(strings.TrimLeft(old, " \t"))]
			replaced := indent + `color: "` + color + `"`
			if colorVal.LineComment != "" {
				replaced += " " + colorVal.LineComment
			}
			lines[idx] = replaced
		}
		return strings.Join(lines, "\n"), nil
	}

	if color == "" {
		return strings.Join(lines, "\n"), nil
	}

	// Insert before the next key at the same or shallower indent (end of this
	// process block), backing up over blank and comment lines.
	boundary := len(lines)
	walkKeys(doc, func(k *yaml.Node) {
		if k.Line > procKey.Line && k.Column <= procKey.Column && k.Line-1 < boundary {
			boundary = k.Line - 1
		}
	})
	insert := boundary
	for insert > procKey.Line {
		prev := strings.TrimSpace(lines[insert-1])
		if prev == "" || strings.HasPrefix(prev, "#") {
			insert--
		} else {
			break
		}
	}
	if len(procVal.Content) == 0 || procVal.Content[0].Column < 1 {
		return "", errors.New("cannot determine field indent")
	}
	indent := strings.Repeat(" ", procVal.Content[0].Column-1)
	newLine := indent + `color: "` + color + `"`
	lines = append(lines[:insert], append([]string{newLine}, lines[insert:]...)...)
	return strings.Join(lines, "\n"), nil
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

func encodeConfigColor(root, proc *yaml.Node, color string) (string, error) {
	setOrRemoveColor(proc, color)
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

func setOrRemoveColor(proc *yaml.Node, color string) {
	for i := 0; i+1 < len(proc.Content); i += 2 {
		if proc.Content[i].Value != "color" {
			continue
		}
		if color == "" {
			proc.Content = append(proc.Content[:i], proc.Content[i+2:]...)
		} else {
			v := proc.Content[i+1]
			v.SetString(color)
			// Hex colors start with `#`, which would read as a comment when
			// unquoted; force quoting.
			v.Style = yaml.DoubleQuotedStyle
		}
		return
	}
	if color == "" {
		return
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "color"}
	val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: color, Style: yaml.DoubleQuotedStyle}
	proc.Content = append(proc.Content, key, val)
}
