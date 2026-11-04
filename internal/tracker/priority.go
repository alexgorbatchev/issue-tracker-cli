package tracker

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// priorityPrefix starts a priority label such as priority:P1, where P0 is the most
// urgent. Priority stays a label, which github.com sets in one click and every
// ticket read already returns, so ordering by it costs no request.
const priorityPrefix = "priority:"

// unprioritized ranks a ticket with no priority label after every labelled one.
const unprioritized = -1

// priorityRank reads a label such as priority:P1 as 1. Label names ignore case on
// GitHub, so priority:p1 counts too.
func priorityRank(label string) (int, bool) {
	if len(label) < len(priorityPrefix) || !strings.EqualFold(label[:len(priorityPrefix)], priorityPrefix) {
		return 0, false
	}
	level := label[len(priorityPrefix):]
	if len(level) < 2 || (level[0] != 'P' && level[0] != 'p') || strings.Trim(level[1:], "0123456789") != "" {
		return 0, false
	}
	rank, err := strconv.Atoi(level[1:])
	return rank, err == nil
}

// parsePriority reads a priority such as P1, ignoring case.
func parsePriority(value string) (string, error) {
	rank, ok := priorityRank(priorityPrefix + value)
	if !ok {
		return "", fmt.Errorf("invalid priority %q: expected P and a number, such as P1", value)
	}
	return priorityName(rank), nil
}

func priorityName(rank int) string {
	return "P" + strconv.Itoa(rank)
}

// IsPriorityLabel reports a label that sets a ticket's priority.
func IsPriorityLabel(label string) bool {
	_, ok := priorityRank(label)
	return ok
}

// priorityRanks lists the distinct priorities of the ticket's labels, most urgent first.
func (t Ticket) priorityRanks() []int {
	ranks := []int{}
	for _, label := range t.Labels {
		if rank, ok := priorityRank(label); ok && !slices.Contains(ranks, rank) {
			ranks = append(ranks, rank)
		}
	}
	slices.Sort(ranks)
	return ranks
}

func (t Ticket) priorityRank() int {
	if ranks := t.priorityRanks(); len(ranks) > 0 {
		return ranks[0]
	}
	return unprioritized
}

// Priority is the most urgent of the ticket's priorities, such as P1, or empty
// when it has none. A ticket with several counts as its most urgent one.
func (t Ticket) Priority() string {
	if rank := t.priorityRank(); rank != unprioritized {
		return priorityName(rank)
	}
	return ""
}

// PriorityConflicts lists the ticket's priorities when it has more than one, most
// urgent first, and nothing otherwise.
func (t Ticket) PriorityConflicts() []string {
	ranks := t.priorityRanks()
	if len(ranks) < 2 {
		return nil
	}
	names := make([]string, 0, len(ranks))
	for _, rank := range ranks {
		names = append(names, priorityName(rank))
	}
	return names
}

// OtherLabels are the ticket's labels without its priority labels, which lists
// show in a column of their own.
func (t Ticket) OtherLabels() []string {
	return slices.DeleteFunc(slices.Clone(t.Labels), IsPriorityLabel)
}

// mostUrgentFirst orders tickets by priority and keeps the existing order, oldest
// first, within each priority. A ticket with no priority comes last.
func mostUrgentFirst(tickets []Ticket) {
	slices.SortStableFunc(tickets, func(a, b Ticket) int {
		ra, rb := a.priorityRank(), b.priorityRank()
		switch {
		case ra == rb:
			return 0
		case ra == unprioritized:
			return 1
		case rb == unprioritized:
			return -1
		}
		return ra - rb
	})
}
