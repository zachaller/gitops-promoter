package utils

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
)

var sanitizedSprigFuncMap = sprig.GenericFuncMap()

func init() {
	delete(sanitizedSprigFuncMap, "env")
	delete(sanitizedSprigFuncMap, "expandenv")
	delete(sanitizedSprigFuncMap, "getHostByName")
	sanitizedSprigFuncMap["urlQueryEscape"] = url.QueryEscape
}

// maxCachedTemplates bounds the process-wide parsed-template cache. Templates come from CR specs,
// so the set in use is small; the cache is simply reset when full rather than tracking recency.
const maxCachedTemplates = 1024

// templateCache memoizes parsed templates by source text and options. Parsing a template copies
// the whole sprig FuncMap into it, which dominated the cost of rendering on every reconcile. A
// parsed template is never mutated after it is cached, and Execute is safe for concurrent use.
type templateCache struct {
	templates map[templateKey]*template.Template
	mu        sync.Mutex
}

// templateKey identifies one parsed template: its source text and the options applied to it.
type templateKey struct {
	text    string
	options string
}

var parsedTemplates = &templateCache{templates: make(map[templateKey]*template.Template)}

func (c *templateCache) get(templateStr string, options []string) (*template.Template, error) {
	key := templateKey{text: templateStr, options: strings.Join(options, "\x00")}

	c.mu.Lock()
	tmpl, ok := c.templates[key]
	c.mu.Unlock()
	if ok {
		return tmpl, nil
	}

	tmpl, err := template.New("").Funcs(sanitizedSprigFuncMap).Parse(templateStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	// Apply options to the template
	for _, option := range options {
		tmpl = tmpl.Option(option)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.templates) >= maxCachedTemplates {
		clear(c.templates)
	}
	c.templates[key] = tmpl
	return tmpl, nil
}

// RenderStringTemplate renders a string template with the provided data.
func RenderStringTemplate(templateStr string, data any, options ...string) (string, error) {
	tmpl, err := parsedTemplates.get(templateStr, options)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}
