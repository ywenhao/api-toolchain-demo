package tasks

import (
	"context"
	pb "example.com/api-toolchain-demo/gen/demo/admin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// AdminServer 将 admin 端协议转换成共用业务层调用。
type AdminServer struct {
	pb.UnimplementedAdminTaskServiceServer
	Store *Store
}

func (s *AdminServer) CreateTask(ctx context.Context, r *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
	if err := grpc.SetHeader(ctx, metadata.Pairs("x-http-code", "201")); err != nil {
		return nil, status.Error(codes.Internal, "设置响应状态失败")
	}
	task, err := s.Store.Create(r.Title, r.Description)
	if err != nil {
		return nil, err
	}
	return &pb.CreateTaskResponse{Task: task}, nil
}
func (s *AdminServer) ListTasks(_ context.Context, r *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	items, total, err := s.Store.List(r.Completed, r.Limit, r.Offset)
	if err != nil {
		return nil, err
	}
	return &pb.ListTasksResponse{Tasks: items, Total: total}, nil
}
func (s *AdminServer) GetTask(_ context.Context, r *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	task, err := s.Store.Get(r.Id)
	if err != nil {
		return nil, err
	}
	return &pb.GetTaskResponse{Task: task}, nil
}
func (s *AdminServer) UpdateTask(_ context.Context, r *pb.UpdateTaskRequest) (*pb.UpdateTaskResponse, error) {
	task, err := s.Store.Update(r.Id, r.Title, r.Description, r.Completed)
	if err != nil {
		return nil, err
	}
	return &pb.UpdateTaskResponse{Task: task}, nil
}
func (s *AdminServer) DeleteTask(_ context.Context, r *pb.DeleteTaskRequest) (*pb.DeleteTaskResponse, error) {
	if err := s.Store.Delete(r.Id); err != nil {
		return nil, err
	}
	return &pb.DeleteTaskResponse{Id: r.Id, Deleted: true}, nil
}

func (s *AdminServer) GetStats(_ context.Context, _ *pb.GetStatsRequest) (*pb.GetStatsResponse, error) {
	total, completed := s.Store.Counts()
	subscribers, id := s.Store.Events.Stats()
	return &pb.GetStatsResponse{Total: total, Completed: completed, Subscribers: subscribers, LastEventId: id}, nil
}
