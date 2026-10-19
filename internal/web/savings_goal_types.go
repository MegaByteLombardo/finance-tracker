package web

import (
	"errors"
	"time"
)

// M7.3: savings goals as the presentation layer sees them.

var (
	// ErrSavingsGoalNotFound: no goal with that id.
	ErrSavingsGoalNotFound = errors.New("savings goal not found")
	// ErrSavingsGoalNotAccepting: the goal is paused or closed.
	ErrSavingsGoalNotAccepting = errors.New("the savings goal does not accept money")
	// ErrSavingsDistributionExceeded: the goals' shares would pass 100%.
	ErrSavingsDistributionExceeded = errors.New("the goals' shares would exceed 100%")
)

// SavingsGoalAccount is the part of a goal's money one account backs.
type SavingsGoalAccount struct {
	AccountID         int64
	AccountName       string
	ReservedFormatted string
}

// SavingsGoalTarget is one target a goal has had.
type SavingsGoalTarget struct {
	TargetFormatted string
	CreatedAt       time.Time
}

// SavingsGoal is a goal with every figure formatted and every action decided.
type SavingsGoal struct {
	ID        int64
	Name      string
	Type      string
	TypeLabel string
	IsOneTime bool

	TargetFormatted         string
	OriginalTargetFormatted string
	// TargetChanged: the current target is not the original budget.
	TargetChanged bool
	// TargetAmount is the target without separators, to prefill a form.
	TargetAmount string
	TargetDate   *time.Time

	DistributionFormatted string
	HasShare              bool

	Status       string
	StatusLabel  string
	FundingLabel string
	FundingBadge string

	ReservedFormatted  string
	RemainingFormatted string
	ProgressFormatted  string
	// ProgressWidth is the progress bar's width in whole percent, 0 to 100,
	// computed by the backend (TD-070: integer geometry).
	ProgressWidth int

	// SuggestedContributionFormatted is empty when the goal has none.
	SuggestedContributionFormatted string

	ClosedAt    *time.Time
	ClosingNote *string

	Closed       bool
	AcceptsMoney bool
	HasMoney     bool
	CanEdit      bool
	CanPause     bool
	CanResume    bool
	CanComplete  bool
	CanCancel    bool

	Accounts        []SavingsGoalAccount
	TargetRevisions []SavingsGoalTarget
	Operations      []SavingsOperation
}

// SavingsGoalInput creates or edits a goal.
type SavingsGoalInput struct {
	Name         string
	Type         string
	Target       string
	TargetDate   string
	Distribution string
}

// SavingsCloseGoalInput completes or cancels a goal.
type SavingsCloseGoalInput struct {
	RequestKey string
	Note       string
}

// SavingsReallocateInput moves reserved money between goals of one account. A
// nil goal is the unallocated part.
type SavingsReallocateInput struct {
	RequestKey string
	AccountID  int64
	FromGoalID *int64
	ToGoalID   *int64
	Amount     string
	Reason     string
}

// SavingsReserveLine is one part of a reservation: an amount for a goal, or
// for the unallocated part when GoalID is nil.
type SavingsReserveLine struct {
	GoalID *int64
	Amount string
}

// SavingsDistributionLine is one line of a suggested distribution.
type SavingsDistributionLine struct {
	GoalID   *int64
	GoalName string
	// Amount is plain, to prefill an input.
	Amount string
	Capped bool
}

// SavingsGoalsView is the goals page.
type SavingsGoalsView struct {
	Available bool
	Error     string

	Goals      []SavingsGoal
	ShowClosed bool
	Return     string
	Alert      *FlashAlert
	Form       SavingsGoalInput
	FormErrors map[string]string
	Navigation MobileNavView
}

// Help explains a savings term.
func (SavingsGoalsView) Help(key string) RecapHelp { return SavingsHelp(key) }

// SavingsGoalView is one goal's page.
type SavingsGoalView struct {
	Goal SavingsGoal
	Fund SavingsFund
	// OtherGoals are the goals money can be moved to.
	OtherGoals []SavingsGoal

	// Fresh idempotency tokens, one per form.
	ContributeKey string
	WithdrawKey   string
	MoveKey       string
	CloseKey      string

	Return     string
	Alert      *FlashAlert
	Navigation MobileNavView
}

// Help explains a savings term.
func (SavingsGoalView) Help(key string) RecapHelp { return SavingsHelp(key) }
