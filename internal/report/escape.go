package report

import (
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/openstatushq/stromboli/internal/store/db"
)

// Slack parses <...> in message text as control sequences: <!channel>,
// <@U123>, <https://evil|Runbook>. Alert titles, log lines and the model's
// output are untrusted, so every such string is escaped before it is posted.
// See https://api.slack.com/reference/surfaces/formatting#escaping
var (
	slackEscaper   = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	slackUnescaper = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">")
)

// esc escapes untrusted text for Slack mrkdwn.
func esc(s string) string { return slackEscaper.Replace(s) }

// unesc undoes esc, for plain_text objects, which Slack shows verbatim and
// never parses for mentions or links.
func unesc(s string) string { return slackUnescaper.Replace(s) }

// safeInv is inv with its untrusted text escaped for Slack.
func safeInv(inv db.Investigation) db.Investigation {
	inv.Title = esc(inv.Title)
	inv.Error.String = esc(inv.Error.String)
	services := make([]string, len(inv.Services))
	for i, s := range inv.Services {
		services[i] = esc(s)
	}
	inv.Services = services
	return inv
}

// safeAction is a with its untrusted text escaped for Slack.
func safeAction(a db.Action) db.Action {
	a.Result.String = esc(a.Result.String)
	a.Error.String = esc(a.Error.String)
	a.Note.String = esc(a.Note.String)
	return a
}

// safeProto returns a copy of m with every string field, at any depth,
// escaped for Slack. Covering fields generically keeps new fields safe.
func safeProto[M proto.Message](m M) M {
	if !m.ProtoReflect().IsValid() {
		return m
	}
	c := proto.Clone(m).(M)
	escapeMessage(c.ProtoReflect())
	return c
}

func escapeMessage(m protoreflect.Message) {
	// Collect first: the message must not be mutated while ranging over it.
	var fields []protoreflect.FieldDescriptor
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		fields = append(fields, fd)
		return true
	})
	for _, fd := range fields {
		switch {
		case fd.IsMap():
			// No map fields today; leave them alone rather than guess.
		case fd.IsList():
			l := m.Mutable(fd).List()
			for i := range l.Len() {
				switch fd.Kind() {
				case protoreflect.StringKind:
					l.Set(i, protoreflect.ValueOfString(esc(l.Get(i).String())))
				case protoreflect.MessageKind, protoreflect.GroupKind:
					escapeMessage(l.Get(i).Message())
				}
			}
		case fd.Kind() == protoreflect.StringKind:
			m.Set(fd, protoreflect.ValueOfString(esc(m.Get(fd).String())))
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			escapeMessage(m.Mutable(fd).Message())
		}
	}
}
