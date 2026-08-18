package migrate

import "net/url"

func pathUnescape(value string) (string, error) { return url.PathUnescape(value) }
