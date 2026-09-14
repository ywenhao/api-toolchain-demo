package tasks

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	modelv1 "example.com/api-toolchain-demo/gen/demo/model/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store 是 user/admin 共用的内存业务层，不包含路由或 HTTP 响应包装。
type Store struct {
	mu     sync.RWMutex
	items  map[string]*modelv1.Task
	nextID uint64
	Events *Broker
}

func NewStore() *Store                       { return &Store{items: make(map[string]*modelv1.Task), Events: NewBroker()} }
func copyTask(t *modelv1.Task) *modelv1.Task { return proto.Clone(t).(*modelv1.Task) }
func validateTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if n := utf8.RuneCountInString(title); n < 1 || n > 120 {
		return "", status.Error(codes.InvalidArgument, "title 去除首尾空白后必须为 1～120 个字符")
	}
	return title, nil
}
func validateDescription(description string) error {
	if utf8.RuneCountInString(description) > 1000 {
		return status.Error(codes.InvalidArgument, "description 最多 1000 个字符")
	}
	return nil
}
func (s *Store) Create(title, description string) (*modelv1.Task, error) {
	title, err := validateTitle(title)
	if err != nil {
		return nil, err
	}
	if err := validateDescription(description); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	now := timestamppb.New(time.Now().UTC())
	task := &modelv1.Task{Id: fmt.Sprintf("task-%06d", s.nextID), Title: title, Description: description, CreatedAt: now, UpdatedAt: now}
	s.items[task.Id] = task
	s.Events.Publish("task.created", task)
	return copyTask(task), nil
}
func (s *Store) List(completed *bool, limit, offset uint32) ([]*modelv1.Task, uint32, error) {
	if limit == 0 {
		limit = 20
	}
	if limit > 100 {
		return nil, 0, status.Error(codes.InvalidArgument, "limit 最大为 100")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]*modelv1.Task, 0, len(s.items))
	for _, task := range s.items {
		if completed != nil && task.Completed != *completed {
			continue
		}
		items = append(items, copyTask(task))
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].CreatedAt.AsTime(), items[j].CreatedAt.AsTime()
		if a.Equal(b) {
			return items[i].Id < items[j].Id
		}
		return a.Before(b)
	})
	total := uint32(len(items))
	start := min(offset, total)
	end := min(uint64(start)+uint64(limit), uint64(total))
	return items[start:end], total, nil
}
func (s *Store) Get(id string) (*modelv1.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.items[id]
	if !ok {
		return nil, status.Error(codes.NotFound, "任务不存在")
	}
	return copyTask(task), nil
}
func (s *Store) Update(id string, title, description *string, completed *bool) (*modelv1.Task, error) {
	if title == nil && description == nil && completed == nil {
		return nil, status.Error(codes.InvalidArgument, "至少提供 title、description 或 completed 中的一个字段")
	}
	if title != nil {
		value, err := validateTitle(*title)
		if err != nil {
			return nil, err
		}
		title = &value
	}
	if description != nil {
		if err := validateDescription(*description); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.items[id]
	if !ok {
		return nil, status.Error(codes.NotFound, "任务不存在")
	}
	task := copyTask(previous)
	if title != nil {
		task.Title = *title
	}
	if description != nil {
		task.Description = *description
	}
	if completed != nil {
		task.Completed = *completed
	}
	task.UpdatedAt = timestamppb.Now()
	s.items[id] = task
	s.Events.Publish("task.updated", task)
	return copyTask(task), nil
}
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.items[id]
	if !ok {
		return status.Error(codes.NotFound, "任务不存在")
	}
	delete(s.items, id)
	s.Events.Publish("task.deleted", task)
	return nil
}
func (s *Store) Counts() (uint32, uint32) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var completed uint32
	for _, task := range s.items {
		if task.Completed {
			completed++
		}
	}
	return uint32(len(s.items)), completed
}
