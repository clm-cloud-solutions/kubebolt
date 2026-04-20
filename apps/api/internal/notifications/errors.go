package notifications

import "errors"

// ErrNoSuchChannel is returned by Manager.SendTest when the requested channel
// is not configured (e.g. user requests a Slack test but no Slack webhook is set).
var errNoSuchChannel = errors.New("notification channel not configured")

// ErrNoSuchChannel is the exported sentinel for callers that need to compare.
// We keep the lowercase version as the internal default; this one is for errors.Is.
var ErrNoSuchChannel = errNoSuchChannel
