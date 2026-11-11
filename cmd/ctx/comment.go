package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const commentUsage = `usage: ctx comment <command>

  add <REQ-n|IDEA-n|PAIN-n> <text> [--json]   write a comment
  list <REQ-n|IDEA-n|PAIN-n> [--json]         list the comments, oldest first
  edit <comment-id> <text> [--json]           replace the text of your own comment (earlier versions are kept)
  delete <comment-id> [--json]                delete a comment (it stays as a placeholder)
  versions <comment-id> [--json]              the versions of a comment

Earlier versions stay visible to every reader; delete what must no longer be seen.`

func cmdComment(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	if len(args) < 2 {
		fmt.Fprintln(stdout, commentUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := anchoredClient(cfg)
	out := func(data any, err error) int { return writeRequestCLIResult(stdout, jsonMode, data, nil, err) }
	one := func(cm store.Comment, err error) int {
		card := cliBuilder(cfg, cm.Project).CommentCard(cm)
		return writeRequestCLIResultCard(stdout, jsonMode, cm, &card, err)
	}
	commentID := func() (int64, bool) {
		n, err := strconv.ParseInt(strings.TrimPrefix(rest[0], "#"), 10, 64)
		return n, err == nil && n > 0
	}
	switch sub {
	case "add", "list":
		ref, err := requestdomain.ParseOriginRef(rest[0])
		if err != nil || (ref.Kind != requestdomain.OriginRequest && ref.Kind != requestdomain.OriginIdea && ref.Kind != requestdomain.OriginPain) {
			fmt.Fprintln(stdout, commentUsage)
			return 2
		}
		if sub == "list" {
			list, err := c.Comments(ref.Kind, ref.ID)
			if err == nil && !jsonMode {
				printComments(stdout, list)
				return 0
			}
			return out(list, err)
		}
		if len(rest) < 2 {
			fmt.Fprintln(stdout, commentUsage)
			return 2
		}
		cm, err := c.AddComment(ref.Kind, ref.ID, strings.Join(rest[1:], " "), 0)
		return one(cm, err)
	case "edit":
		id, ok := commentID()
		if !ok || len(rest) < 2 {
			fmt.Fprintln(stdout, commentUsage)
			return 2
		}
		cm, err := c.EditComment(id, strings.Join(rest[1:], " "))
		return one(cm, err)
	case "delete":
		id, ok := commentID()
		if !ok {
			fmt.Fprintln(stdout, commentUsage)
			return 2
		}
		cm, err := c.DeleteComment(id)
		return one(cm, err)
	case "versions":
		id, ok := commentID()
		if !ok {
			fmt.Fprintln(stdout, commentUsage)
			return 2
		}
		list, err := c.CommentVersions(id)
		return out(list, err)
	}
	fmt.Fprintln(stdout, commentUsage)
	return 2
}

func printComments(w io.Writer, list []store.Comment) {
	if len(list) == 0 {
		fmt.Fprintln(w, "no comments")
		return
	}
	for _, c := range list {
		if c.Deleted {
			fmt.Fprintf(w, "#%d  (deleted %s)\n", c.ID, c.DeletedAt)
			continue
		}
		edited := ""
		if c.EditedAt != "" {
			edited = ", edited " + c.EditedAt
		}
		via := ""
		if c.Via != "" {
			via = ", via " + c.Via
		}
		fmt.Fprintf(w, "#%d  %s, %s%s%s\n%s\n\n", c.ID, c.Person, c.CreatedAt, edited, via, c.Body)
	}
}
