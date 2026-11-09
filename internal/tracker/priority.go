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

const (
	// lowestPriority is P3. Any other priority:* label is an ordinary label.
	lowestPriority = 3
	// unprioritized ranks a ticket with no priority label after every labelled one.
	unprioritized = -1
)

// priorityRank reads a label such as priority:P1 as 1. Label names ignore case on
// GitHub, so priority:p1 counts too.
func priorityRank(label string) (int, bool) {
	if len(label) != len(priorityPrefix)+2 || !strings.EqualFold(label[:len(priorityPrefix)], priorityPrefix) {
		return 0, false
	}
	level, digit := label[len(priorityPrefix)], label[len(priorityPrefix)+1]
	if (level != 'P' && level != 'p') || digit < '0' || digit > '0'+lowestPriority {
		return 0, false
	}
	return int(digit - '0'), true
}

// parsePriority reads a priority such as P1, ignoring case.
func parsePriority(value string) (string, error) {
	rank, ok := priorityRank(priorityPrefix + value)
	if !ok {
		return "", fmt.Errorf("invalid priority %q: expected P0, P1, P2, or P3", value)
	}
	return priorityName(rank), nil
}

func priorityName(rank int) string {
	return "P" + strconv.Itoa(rank)
}

// isPriorityLabel reports a label that sets a ticket's priority.
func isPriorityLabel(label string) bool {
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
	return slices.DeleteFunc(slices.Clone(t.Labels), isPriorityLabel)
}

// mostUrgentFirst orders tickets by priority. Within a priority, the tickets of an
// active epic, one with a ticket in Doing, come first, so that agents finish an epic
// before starting the next; otherwise the existing order, oldest first, stays. A
// ticket with no priority comes last.
func mostUrgentFirst(tickets []Ticket, activeEpics map[int]bool) {
	ranks := make(map[int]int, len(tickets))
	for _, ticket := range tickets {
		ranks[ticket.Number] = ticket.priorityRank()
	}
	active := func(t Ticket) bool { return t.ParentEpic && activeEpics[t.Parent] }
	slices.SortStableFunc(tickets, func(a, b Ticket) int {
		ra, rb := ranks[a.Number], ranks[b.Number]
		switch {
		case ra == rb && active(a) == active(b):
			return 0
		case ra == rb && active(a):
			return -1
		case ra == rb:
			return 1
		case ra == unprioritized:
			return 1
		case rb == unprioritized:
			return -1
		}
		return ra - rb
	})
}
