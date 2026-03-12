package redishandler

import "github.com/go-redis/redis/v8"

type Handler struct {
    Redis *redis.Client
}

func NewHandler(rdb *redis.Client) *Handler {
    return &Handler{Redis: rdb}
}