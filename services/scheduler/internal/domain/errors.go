package domain

import "errors"

var (
	ErrJobNotFound        = errors.New("job not found")
	ErrJobAlreadyExists   = errors.New("job already exists")
	ErrExecutionNotFound  = errors.New("execution not found")
	ErrTaskNotFound       = errors.New("task not found")
	ErrJobLocked          = errors.New("job is locked")
	ErrInvalidCron        = errors.New("invalid cron expression")
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
	// ErrPlatformJob: los trabajos de plataforma (sin empresa) los define la plataforma;
	// por el API de una empresa se leen, pero no se cambian ni se lanzan.
	ErrPlatformJob = errors.New("platform jobs cannot be changed from a tenant")
)
