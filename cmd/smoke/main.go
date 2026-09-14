// smoke 从外部调用已启动的服务，检查双端 CRUD、文档隔离和 SSE。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type frame struct{ Kind, ID, Data string }
type stream struct {
	frames chan frame
	cancel context.CancelFunc
	body   io.ReadCloser
}
type tester struct {
	base   string
	client *http.Client
	passed int
}

func require(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}
func object(value any) map[string]any {
	v, ok := value.(map[string]any)
	require(ok, "expected object, got %T", value)
	return v
}
func (t *tester) pass(label string) { t.passed++; fmt.Println("✓", label) }
func (t *tester) request(method, path string, body any, want int) map[string]any {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require(err == nil, "encode body: %v", err)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, t.base+path, reader)
	require(err == nil, "request: %v", err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := t.client.Do(req)
	require(err == nil, "%s %s: %v", method, path, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require(err == nil, "read response: %v", err)
	require(response.StatusCode == want, "%s %s: wanted %d, got %d: %s", method, path, want, response.StatusCode, data)
	var result map[string]any
	if strings.Contains(response.Header.Get("Content-Type"), "json") {
		require(json.Unmarshal(data, &result) == nil, "invalid JSON: %s", data)
	}
	return result
}

func openStream(base, path, cursor string) *stream {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	require(err == nil, "SSE request: %v", err)
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		panic(fmt.Sprintf("connect SSE: %v", err))
	}
	require(response.StatusCode == 200, "SSE HTTP status: %d", response.StatusCode)
	require(strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream"), "invalid SSE Content-Type")
	s := &stream{frames: make(chan frame, 64), cancel: cancel, body: response.Body}
	go func() {
		defer close(s.frames)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		current := frame{}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if current.Data != "" {
					select {
					case s.frames <- current:
					case <-ctx.Done():
						return
					}
				}
				current = frame{}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.TrimPrefix(value, " ")
			switch key {
			case "event":
				current.Kind = value
			case "id":
				current.ID = value
			case "data":
				if current.Data != "" {
					current.Data += "\n"
				}
				current.Data += value
			}
		}
	}()
	return s
}
func (s *stream) close() { s.cancel(); s.body.Close() }
func (s *stream) next(kind, taskID string) frame {
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-s.frames:
			require(ok, "SSE closed before %s", kind)
			if event.Kind != kind {
				continue
			}
			if taskID != "" {
				var payload map[string]any
				require(json.Unmarshal([]byte(event.Data), &payload) == nil, "invalid SSE JSON")
				task, ok := payload["task"].(map[string]any)
				if !ok || task["id"] != taskID {
					continue
				}
			}
			return event
		case <-timer.C:
			panic("timeout waiting for SSE event " + kind)
		}
	}
}

func main() {
	base := flag.String("base-url", "http://127.0.0.1:8088", "已启动的 HTTP 服务地址")
	flag.Parse()
	defer func() {
		if failure := recover(); failure != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", failure)
			os.Exit(1)
		}
	}()
	t := &tester{base: strings.TrimRight(*base, "/"), client: &http.Client{Timeout: 5 * time.Second}}
	t.run()
	fmt.Printf("完成：%d 项 HTTP / SSE 检查通过\n", t.passed)
}

func (t *tester) run() {
	t.request("GET", "/healthz", nil, 200)
	t.pass("健康检查")
	for _, side := range []string{"user", "admin"} {
		doc := t.request("GET", "/openapi/"+side+".json", nil, 200)
		require(doc["openapi"] == "3.0.3", "%s is not OpenAPI 3.0.3", side)
		paths := object(doc["paths"])
		for path := range paths {
			require(path == "/healthz" || strings.HasPrefix(path, "/"+side+"/v1/"), "%s document leaks route %s", side, path)
		}
		responses := object(object(object(paths["/"+side+"/v1/tasks"])["post"])["responses"])
		_, ok := responses["201"]
		require(ok, "create endpoint must document 201")
		_, ok = responses["200"]
		require(!ok, "unexpected default 200 in create endpoint")
		events := object(object(paths["/"+side+"/v1/events"])["get"])
		content := object(object(object(events["responses"])["200"])["content"])
		_, ok = content["text/event-stream"]
		require(ok, "missing SSE content type")
		t.request("GET", "/docs/"+side, nil, 200)
		t.pass(side + " 文档：OpenAPI 3、路由隔离、201 与 SSE")
	}
	user := openStream(t.base, "/user/v1/events", "")
	defer user.close()
	user.next("ready", "")
	admin := openStream(t.base, "/admin/v1/events", "")
	defer admin.close()
	admin.next("ready", "")
	t.pass("user/admin SSE 均建立真实长连接")
	t.request("POST", "/user/v1/tasks", map[string]any{"title": "   "}, 400)
	t.request("POST", "/admin/v1/tasks", map[string]any{"title": "x", "unknownField": true}, 400)
	t.request("GET", "/user/v1/tasks?limit=101", nil, 400)
	t.pass("标题、未知字段、分页错误返回 400")
	created := t.request("POST", "/user/v1/tasks", map[string]any{"title": "smoke " + time.Now().Format("150405.000000000"), "description": "临时验证数据"}, 201)
	task := object(created["task"])
	id, ok := task["id"].(string)
	require(ok && id != "", "missing task id")
	defer func() {
		req, _ := http.NewRequest("DELETE", t.base+"/user/v1/tasks/"+id, nil)
		if r, err := t.client.Do(req); err == nil {
			r.Body.Close()
		}
	}()
	first := user.next("task.created", id)
	other := admin.next("task.created", id)
	require(first.ID != "" && first.ID == other.ID, "event IDs differ across subscribers")
	t.pass("用户创建返回 201，双端收到同一个 task.created")
	got := t.request("GET", "/admin/v1/tasks/"+id, nil, 200)
	require(object(got["task"])["id"] == id, "admin cannot read shared task")
	updated := t.request("PATCH", "/admin/v1/tasks/"+id, map[string]any{"completed": true}, 200)
	require(object(updated["task"])["completed"] == true, "completed was not updated")
	user.next("task.updated", id)
	admin.next("task.updated", id)
	t.pass("管理端可读取和修改共享任务，双端收到更新事件")
	updated = t.request("PATCH", "/user/v1/tasks/"+id, map[string]any{"completed": false, "description": ""}, 200)
	require(object(updated["task"])["completed"] == false && object(updated["task"])["description"] == "", "optional false / empty string lost")
	user.next("task.updated", id)
	admin.next("task.updated", id)
	t.pass("PATCH 保留 optional 的 false 和空字符串语义")
	listed := t.request("GET", "/user/v1/tasks?completed=false&limit=100", nil, 200)
	found := false
	for _, raw := range listed["tasks"].([]any) {
		if object(raw)["id"] == id {
			found = true
		}
	}
	require(found, "filtered list missing task")
	t.pass("分页列表与完成状态过滤")
	replay := openStream(t.base, "/user/v1/events", first.ID)
	replay.next("ready", "")
	a := replay.next("task.updated", id)
	b := replay.next("task.updated", id)
	require(a.ID != b.ID, "duplicate replay ID")
	replay.close()
	t.pass("Last-Event-ID 重连重放两次更新")
	t.request("GET", "/admin/v1/events?last_event_id=invalid", nil, 400)
	reset := openStream(t.base, "/admin/v1/events", "18446744073709551615")
	reset.next("ready", "")
	reset.next("reset", "")
	reset.close()
	t.pass("非法游标返回 400，超出范围返回 reset")
	stats := t.request("GET", "/admin/v1/stats", nil, 200)
	require(stats["subscribers"].(float64) >= 2, "SSE subscribers missing from stats")
	t.request("GET", "/user/v1/stats", nil, 404)
	t.pass("统计接口仅存在于 admin 路由")
	deleted := t.request("DELETE", "/admin/v1/tasks/"+id, nil, 200)
	require(deleted["deleted"] == true, "delete response missing flag")
	user.next("task.deleted", id)
	admin.next("task.deleted", id)
	t.request("GET", "/user/v1/tasks/"+id, nil, 404)
	t.pass("管理端删除，双端收到删除事件，再次读取返回 404")
	// 再由管理端创建、用户端修改删除，覆盖另一组 CRUD 映射。
	second := object(t.request("POST", "/admin/v1/tasks", map[string]any{"title": "admin smoke"}, 201)["task"])["id"].(string)
	t.request("GET", "/user/v1/tasks/"+second, nil, 200)
	t.request("PATCH", "/user/v1/tasks/"+second, map[string]any{"title": "updated by user"}, 200)
	t.request("DELETE", "/user/v1/tasks/"+second, nil, 200)
	t.pass("另一方向的 admin 创建 / user 修改删除")
}
