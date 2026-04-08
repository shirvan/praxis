package provider

import restate "github.com/restatedev/sdk-go"

// PreDeleter runs cleanup before a resource delete.
type PreDeleter interface {
	PreDelete(ctx restate.Context, key string) error
}

// PostDeleter runs cleanup after a resource has been deleted.
type PostDeleter interface {
	PostDelete(ctx restate.Context, key string) error
}