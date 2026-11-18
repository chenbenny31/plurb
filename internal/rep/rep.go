// Package rep holds the reputation rules: how much a vote weighs, how much
// reputation it moves, and the caps that blunt vote rings. Pure functions,
// so the policy can be read and tested in one place; the store applies it
// transactionally.
package rep

import (
	"math"
	"time"
)

const (
	// Accounts younger than this vote with weight 0. The vote is recorded,
	// but since weights are snapshotted it never counts, so freshly made
	// sockpuppets can't pump scores.
	MinAccountAge = 24 * time.Hour
	// MaxWeight bounds any single voter's influence.
	MaxWeight = 5.0
	// DownvoteMinRep gates downvoting behind some earned trust.
	DownvoteMinRep = 15

	ThreadUpvote   = 5
	CommentUpvote  = 2
	DownvoteReward = -1

	// PerVoterDailyCap limits the net reputation one voter can give one
	// author per 24h, so a pair of friends can't farm each other.
	PerVoterDailyCap = 10
	// PerUserDailyCap limits total reputation an author can gain per 24h.
	PerUserDailyCap = 200
)

// Weight is a voter's influence, fixed at the moment they vote:
// 0 for new accounts, then 1 + log2(1 + rep/100), capped at MaxWeight.
// Logarithmic so established users count more without dominating.
func Weight(reputation int, accountAge time.Duration) float64 {
	if accountAge < MinAccountAge {
		return 0
	}
	if reputation < 0 {
		reputation = 0
	}
	return math.Min(MaxWeight, 1+math.Log2(1+float64(reputation)/100))
}

// Reward is the reputation a vote of value (+1/-1) moves for the target's
// author, before caps. Zero-weight voters move nothing.
func Reward(targetType string, value int, weight float64) int {
	if weight == 0 || value == 0 {
		return 0
	}
	if value < 0 {
		return DownvoteReward
	}
	if targetType == "thread" {
		return ThreadUpvote
	}
	return CommentUpvote
}

// Capped trims a positive reward so that neither the voter->author net for
// the last day nor the author's total for the last day exceeds its cap.
// Negative rewards are never capped.
func Capped(reward, fromVoterToday, authorToday int) int {
	if reward <= 0 {
		return reward
	}
	room := min(PerVoterDailyCap-fromVoterToday, PerUserDailyCap-authorToday)
	return max(0, min(reward, room))
}

// Gravity controls how fast threads sink in the "hot" ordering.
const Gravity = 1.5

// Hot is the time-decayed ranking score: weighted score divided by a power
// of age, so a strong new thread outranks an old one with the same score.
// The store computes the same expression in SQL; this is its reference.
func Hot(score float64, age time.Duration) float64 {
	return score / math.Pow(age.Hours()+2, Gravity)
}

// ---- levels ----
//
// Reputation is shown as a level, not a number: where a user's reputation
// sits in the current distribution. Levels are relative, so they stay
// meaningful as the community (and point totals) grow.

// LevelPercentiles are the distribution cut points for levels 2..5 among
// users with positive reputation: above the median, top 20%, 5% and 1%.
var LevelPercentiles = [4]float64{0.50, 0.80, 0.95, 0.99}

// LevelFloors is the minimum reputation for levels 1..5, so a tiny
// community can't put its only active member straight at the top.
var LevelFloors = [5]int{1, 10, 50, 200, 500}

// Levels is a snapshot of the distribution, recomputed periodically.
type Levels struct {
	Cuts  [4]int // reputation at each of LevelPercentiles
	Users int    // users with positive reputation
}

// Of returns the level (0..5) for a reputation: the lower of where it sits
// in the distribution and what its absolute floor allows.
func (l Levels) Of(reputation int) int {
	if reputation < LevelFloors[0] {
		return 0
	}
	byRank := 1
	for i, cut := range l.Cuts {
		if l.Users > 0 && reputation >= cut {
			byRank = i + 2
		}
	}
	byFloor := 0
	for i, floor := range LevelFloors {
		if reputation >= floor {
			byFloor = i + 1
		}
	}
	return min(byRank, byFloor)
}

// LevelName is the short label shown next to names.
func LevelName(level int) string {
	if level == 0 {
		return "New"
	}
	return "Lv " + string(rune('0'+level))
}

// LevelBand describes a level for tooltips.
func LevelBand(level int) string {
	switch level {
	case 5:
		return "top 1% of contributors"
	case 4:
		return "top 5% of contributors"
	case 3:
		return "top 20% of contributors"
	case 2:
		return "top half of contributors"
	case 1:
		return "contributor"
	}
	return "no reputation yet"
}
