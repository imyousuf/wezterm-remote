// Package web contains the offline browser UI embedded in WezRelay.
package web

import "embed"

// Assets includes the application and vendored terminal libraries.
//
//go:embed index.html app.js style.css vendor
var Assets embed.FS
