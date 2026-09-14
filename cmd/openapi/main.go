// 合并非 proto HTTP 路由，输出可直接交给 Swagger UI 的 OpenAPI 3 JSON。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	for _, side := range []string{"user", "admin"} {
		if err := runSide(side); err != nil {
			return err
		}
	}
	return nil
}

func runSide(side string) error {
	data, err := os.ReadFile("openapi/" + side + "/openapi.yaml")
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if doc["openapi"] != "3.0.3" {
		return fmt.Errorf("unexpected OpenAPI version: %v", doc["openapi"])
	}
	data, err = os.ReadFile("docs/http.openapi.json")
	if err != nil {
		return err
	}
	var extra map[string]any
	if err := json.Unmarshal(data, &extra); err != nil {
		return err
	}
	paths := object(doc, "paths")
	for path, item := range object(extra, "paths") {
		if strings.HasPrefix(path, "/v1/") {
			path = "/" + side + path
		}
		if _, exists := paths[path]; exists {
			return fmt.Errorf("duplicate HTTP route in overlay: %s", path)
		}
		paths[path] = item
	}
	schemas := object(object(doc, "components"), "schemas")
	for name, schema := range object(object(extra, "components"), "schemas") {
		if _, exists := schemas[name]; !exists {
			schemas[name] = schema
		}
	}
	// proto 已声明 201。移除生成器额外添加的 200，保持与实际响应一致。
	post := object(object(paths, "/"+side+"/v1/tasks"), "post")
	responses := object(post, "responses")
	if _, ok := responses["201"]; !ok {
		return fmt.Errorf("CreateTask must declare HTTP 201 in proto")
	}
	delete(responses, "200")
	// 跟随当前站点的主机和端口，便于本地浏览器调试。
	doc["servers"] = []any{map[string]any{"url": "/"}}
	doc["tags"] = []any{
		map[string]any{"name": "任务", "description": "由 proto 生成的 HTTP CRUD"},
		map[string]any{"name": "实时事件", "description": "原生 HTTP SSE"},
		map[string]any{"name": "系统", "description": "健康检查"},
	}
	if side == "admin" {
		doc["tags"] = append(doc["tags"].([]any), map[string]any{"name": "管理统计", "description": "管理端专用接口"})
	}
	for _, raw := range paths {
		for method, value := range raw.(map[string]any) {
			if !strings.Contains(" get post put patch delete ", " "+method+" ") {
				continue
			}
			op := value.(map[string]any)
			if tags, ok := op["tags"].([]any); ok && len(tags) > 1 {
				var keep []any
				for _, tag := range tags {
					if name, ok := tag.(string); !ok || !strings.HasSuffix(name, "TaskService") {
						keep = append(keep, tag)
					}
				}
				op["tags"] = keep
			}
		}
	}
	if err := validateReferences(doc, doc); err != nil {
		return err
	}
	output, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile("openapi/"+side+".json", append(output, '\n'), 0644); err != nil {
		return err
	}
	fmt.Println(side + " OpenAPI 3.0.3: proto + HTTP 文档已合并")
	return nil
}

func object(parent map[string]any, key string) map[string]any {
	if child, ok := parent[key].(map[string]any); ok {
		return child
	}
	child := map[string]any{}
	parent[key] = child
	return child
}

func validateReferences(root map[string]any, node any) error {
	switch value := node.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			var cursor any = root
			for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
				mapping, ok := cursor.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid OpenAPI reference: %s", ref)
				}
				cursor, ok = mapping[part]
				if !ok {
					return fmt.Errorf("unresolved OpenAPI reference: %s", ref)
				}
			}
		}
		for _, child := range value {
			if err := validateReferences(root, child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateReferences(root, child); err != nil {
				return err
			}
		}
	}
	return nil
}
