// Command pcma-install-config prepares operator configuration without changing live files.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Mapping edits retain comments and unrelated settings. Aliases and merge keys are
// refused rather than editing an anchor that another resource might share.
func validate(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return fmt.Errorf("configuration aliases/anchors require expansion before installation")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Tag != "!!str" || seen[k.Value] || k.Value == "<<" {
				return fmt.Errorf("configuration contains duplicate or unsupported mapping keys")
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := validate(child); err != nil {
			return err
		}
	}
	return nil
}

func get(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func put(n *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			value.HeadComment = n.Content[i+1].HeadComment
			value.LineComment = n.Content[i+1].LineComment
			n.Content[i+1] = value
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}
func mapping(n *yaml.Node, key string) (*yaml.Node, error) {
	v := get(n, key)
	if v == nil {
		v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		put(n, key, v)
	}
	if v.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", key)
	}
	return v, nil
}
func node(v any) *yaml.Node {
	n := new(yaml.Node)
	if err := n.Encode(v); err != nil {
		panic(err)
	}
	return n
}

type plan struct {
	Profiles map[string]string `json:"profiles"`
	Asset    map[string]any    `json:"asset"`
	Setup    map[string]any    `json:"setup"`
}

func run() error {
	config := flag.String("config", "", "existing operator YAML")
	work := flag.String("work", "", "service working directory")
	change := flag.String("plan", "", "JSON merge plan; omit to inspect")
	out := flag.String("output", "", "new staged YAML file")
	flag.Parse()
	f, err := os.Open(*config)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 8<<20 {
		return fmt.Errorf("configuration must be a regular file no larger than 8 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return fmt.Errorf("configuration exceeds 8 MiB")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err = dec.Decode(&doc); err != nil {
		return err
	}
	var extra yaml.Node
	if err = dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one YAML document")
	}
	if err = validate(&doc); err != nil {
		return err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("configuration must be a mapping")
	}
	root := doc.Content[0]
	core, err := mapping(root, "core")
	if err != nil {
		return err
	}
	connectors, err := mapping(core, "connectors")
	if err != nil {
		return err
	}
	// The pinned full host mounts xbuddy when no connector is declared.
	// Making the block explicit must retain that existing default.
	if len(connectors.Content) == 0 {
		put(connectors, "xbuddy", node(map[string]string{"type": "xbuddy"}))
	}
	// An omitted connector block means the shipping xbuddy connector.
	if len(connectors.Content) == 0 {
		put(connectors, "xbuddy", node(map[string]any{"type": "xbuddy"}))
	}
	if *change == "" {
		overlay := filepath.Join(filepath.Dir(*config), "config.d", "resources.json")
		if v := get(core, "overlay"); v != nil && v.Value != "" {
			overlay = v.Value
			if !filepath.IsAbs(overlay) {
				overlay = filepath.Join(*work, overlay)
			}
		}
		var cs, rs any
		if err = connectors.Decode(&cs); err != nil {
			return err
		}
		if v := get(root, "resources"); v != nil {
			if err = v.Decode(&rs); err != nil {
				return err
			}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"connectors": cs, "resources": rs, "overlay": overlay})
	}
	b, err := os.ReadFile(*change)
	if err != nil {
		return err
	}
	var p plan
	if err = json.Unmarshal(b, &p); err != nil {
		return err
	}
	for name, profile := range p.Profiles {
		c, e := mapping(connectors, name)
		if e != nil {
			return e
		}
		put(c, "type", node("pcma-moonraker"))
		settings, e := mapping(c, "config")
		if e != nil {
			return e
		}
		put(settings, "capability_profile_path", node(profile))
		if get(settings, "starts") == nil {
			put(settings, "starts", node(false))
		}
	}
	resources, err := mapping(root, "resources")
	if err != nil {
		return err
	}
	assets, err := mapping(resources, "asset")
	if err != nil {
		return err
	}
	if existing := get(assets, "pcma-tools"); existing != nil {
		t := get(existing, "type")
		if t == nil || t.Value != p.Asset["type"] {
			return fmt.Errorf("asset pcma-tools belongs to another type")
		}
	}
	asset, err := mapping(assets, "pcma-tools")
	if err != nil {
		return err
	}
	for _, key := range []string{"type", "enabled", "required", "asset"} {
		put(asset, key, node(p.Asset[key]))
	}
	if p.Setup != nil {
		sinks, err := mapping(resources, "sink")
		if err != nil {
			return err
		}
		if existing := get(sinks, "pcma-setup"); existing != nil {
			t := get(existing, "type")
			if t == nil || t.Value != p.Setup["type"] {
				return fmt.Errorf("sink pcma-setup belongs to another type")
			}
		}
		put(sinks, "pcma-setup", node(p.Setup))
	}
	fout, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(fout)
	enc.SetIndent(2)
	err = enc.Encode(&doc)
	if e := enc.Close(); err == nil {
		err = e
	}
	if e := fout.Close(); err == nil {
		err = e
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
