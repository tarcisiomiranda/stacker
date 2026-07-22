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
