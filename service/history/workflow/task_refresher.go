// The MIT License
//
// Copyright (c) 2020 Temporal Technologies Inc.  All rights reserved.
//
// Copyright (c) 2020 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

//go:generate mockgen -copyright_file ../../../LICENSE -package $GOPACKAGE -source $GOFILE -destination task_refresher_mock.go

package workflow

import (
	"context"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"

	enumsspb "go.temporal.io/server/api/enums/v1"
	"go.temporal.io/server/common"
	"go.temporal.io/server/common/log"
	"go.temporal.io/server/common/namespace"
	"go.temporal.io/server/common/primitives/timestamp"
	"go.temporal.io/server/service/history/configs"
	"go.temporal.io/server/service/history/events"
	"go.temporal.io/server/service/history/shard"
)

type (
	TaskRefresher interface {
		RefreshTasks(ctx context.Context, mutableState MutableState) error
	}

	TaskRefresherImpl struct {
		shard             shard.Context
		config            *configs.Config
		namespaceRegistry namespace.Registry
		eventsCache       events.Cache
		logger            log.Logger
	}
)

func NewTaskRefresher(
	shard shard.Context,
	config *configs.Config,
	namespaceRegistry namespace.Registry,
	eventsCache events.Cache,
	logger log.Logger,
) *TaskRefresherImpl {

	return &TaskRefresherImpl{
		shard:             shard,
		config:            config,
		namespaceRegistry: namespaceRegistry,
		eventsCache:       eventsCache,
		logger:            logger,
	}
}

func (r *TaskRefresherImpl) RefreshTasks(
	ctx context.Context,
	mutableState MutableState,
) error {

	taskGenerator := taskGeneratorProvider.NewTaskGenerator(
		r.shard,
		mutableState,
	)

	if err := r.refreshTasksForWorkflowStart(
		ctx,
		mutableState,
		taskGenerator,
	); err != nil {
		return err
	}

	if err := r.refreshTasksForWorkflowClose(
		ctx,
		mutableState,
		taskGenerator,
	); err != nil {
		return err
	}

	if err := r.refreshTasksForRecordWorkflowStarted(
		ctx,
		mutableState,
		taskGenerator,
	); err != nil {
		return err
	}

	if err := r.refreshWorkflowTaskTasks(
		mutableState,
		taskGenerator,
	); err != nil {
		return err
	}

	if err := r.refreshTasksForActivity(
		ctx,
		mutableState,
		taskGenerator,
	); err != nil {
		return err
	}

	if err := r.refreshTasksForTimer(
		mutableState,
	); err != nil {
		return err
	}

	if err := r.refreshTasksForChildWorkflow(
		ctx,
		mutableState,
		taskGenerator,
	); err != nil {
		return err
	}

	if err := r.refreshTasksForRequestCancelExternalWorkflow(
		ctx,
		mutableState,
		taskGenerator,
	); err != nil {
		return err
	}

	if err := r.refreshTasksForSignalExternalWorkflow(
		ctx,
		mutableState,
		taskGenerator,
	); err != nil {
		return err
	}

	return r.refreshTasksForWorkflowSearchAttr(taskGenerator)
}

func (r *TaskRefresherImpl) refreshTasksForWorkflowStart(
	ctx context.Context,
	mutableState MutableState,
	taskGenerator TaskGenerator,
) error {

	startEvent, err := mutableState.GetStartEvent(ctx)
	if err != nil {
		return err
	}

	if err := taskGenerator.GenerateWorkflowStartTasks(
		startEvent,
	); err != nil {
		return err
	}

	startAttr := startEvent.GetWorkflowExecutionStartedEventAttributes()
	if !mutableState.HadOrHasWorkflowTask() && timestamp.DurationValue(startAttr.GetFirstWorkflowTaskBackoff()) > 0 {
		if err := taskGenerator.GenerateDelayedWorkflowTasks(
			startEvent,
		); err != nil {
			return err
		}
	}

	return nil
}

func (r *TaskRefresherImpl) refreshTasksForWorkflowClose(
	ctx context.Context,
	mutableState MutableState,
	taskGenerator TaskGenerator,
) error {

	executionState := mutableState.GetExecutionState()

	if executionState.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		closeEvent, err := mutableState.GetCompletionEvent(ctx)
		if err != nil {
			return err
		}

		return taskGenerator.GenerateWorkflowCloseTasks(
			closeEvent,
			false,
		)
	}

	return nil
}

func (r *TaskRefresherImpl) refreshTasksForRecordWorkflowStarted(
	ctx context.Context,
	mutableState MutableState,
	taskGenerator TaskGenerator,
) error {

	startEvent, err := mutableState.GetStartEvent(ctx)
	if err != nil {
		return err
	}

	executionState := mutableState.GetExecutionState()

	if executionState.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		return taskGenerator.GenerateRecordWorkflowStartedTasks(
			startEvent,
		)
	}

	return nil
}

func (r *TaskRefresherImpl) refreshWorkflowTaskTasks(
	mutableState MutableState,
	taskGenerator TaskGenerator,
) error {

	if !mutableState.HasPendingWorkflowTask() {
		// no workflow task at all
		return nil
	}

	workflowTask := mutableState.GetPendingWorkflowTask()
	if workflowTask == nil {
		return serviceerror.NewInternal("it could be a bug, cannot get pending workflow task")
	}

	if workflowTask.Type == enumsspb.WORKFLOW_TASK_TYPE_SPECULATIVE {
		// Do not generate tasks because speculative WT doesn't have any timer or transfer tasks associated with it.
		return nil
	}

	// workflowTask already started
	if workflowTask.StartedEventID != common.EmptyEventID {
		return taskGenerator.GenerateStartWorkflowTaskTasks(
			workflowTask.ScheduledEventID,
		)
	}

	// workflowTask only scheduled
	return taskGenerator.GenerateScheduleWorkflowTaskTasks(
		workflowTask.ScheduledEventID,
		false,
	)
}

func (r *TaskRefresherImpl) refreshTasksForActivity(
	ctx context.Context,
	mutableState MutableState,
	taskGenerator TaskGenerator,
) error {

	executionInfo := mutableState.GetExecutionInfo()
	executionState := mutableState.GetExecutionState()
	pendingActivityInfos := mutableState.GetPendingActivityInfos()

	currentBranchToken, err := mutableState.GetCurrentBranchToken()
	if err != nil {
		return err
	}

Loop:
	for _, activityInfo := range pendingActivityInfos {
		// clear all activity timer task mask for later activity timer task re-generation
		activityInfo.TimerTaskStatus = TimerTaskStatusNone

		// need to update activity timer task mask for which task is generated
		if err := mutableState.UpdateActivity(
			activityInfo,
		); err != nil {
			return err
		}

		if activityInfo.StartedEventId != common.EmptyEventID {
			continue Loop
		}

		scheduleEvent, err := r.eventsCache.GetEvent(
			ctx,
			events.EventKey{
				NamespaceID: namespace.ID(executionInfo.NamespaceId),
				WorkflowID:  executionInfo.WorkflowId,
				RunID:       executionState.RunId,
				EventID:     activityInfo.ScheduledEventId,
				Version:     activityInfo.Version,
			},
			activityInfo.ScheduledEventBatchId,
			currentBranchToken,
		)
		if err != nil {
			return err
		}

		if err := taskGenerator.GenerateActivityTasks(
			scheduleEvent,
		); err != nil {
			return err
		}
	}

	if _, err := NewTimerSequence(
		mutableState,
	).CreateNextActivityTimer(); err != nil {
		return err
	}

	return nil
}

func (r *TaskRefresherImpl) refreshTasksForTimer(
	mutableState MutableState,
) error {

	pendingTimerInfos := mutableState.GetPendingTimerInfos()

	for _, timerInfo := range pendingTimerInfos {
		// clear all timer task mask for later timer task re-generation
		timerInfo.TaskStatus = TimerTaskStatusNone

		// need to update user timer task mask for which task is generated
		if err := mutableState.UpdateUserTimer(
			timerInfo,
		); err != nil {
			return err
		}
	}

	if _, err := NewTimerSequence(
		mutableState,
	).CreateNextUserTimer(); err != nil {
		return err
	}

	return nil
}

func (r *TaskRefresherImpl) refreshTasksForChildWorkflow(
	ctx context.Context,
	mutableState MutableState,
	taskGenerator TaskGenerator,
) error {

	executionInfo := mutableState.GetExecutionInfo()
	executionState := mutableState.GetExecutionState()
	pendingChildWorkflowInfos := mutableState.GetPendingChildExecutionInfos()

	currentBranchToken, err := mutableState.GetCurrentBranchToken()
	if err != nil {
		return err
	}

Loop:
	for _, childWorkflowInfo := range pendingChildWorkflowInfos {
		if childWorkflowInfo.StartedEventId != common.EmptyEventID {
			continue Loop
		}

		scheduleEvent, err := r.eventsCache.GetEvent(
			ctx,
			events.EventKey{
				NamespaceID: namespace.ID(executionInfo.NamespaceId),
				WorkflowID:  executionInfo.WorkflowId,
				RunID:       executionState.RunId,
				EventID:     childWorkflowInfo.InitiatedEventId,
				Version:     childWorkflowInfo.Version,
			},
			childWorkflowInfo.InitiatedEventBatchId,
			currentBranchToken,
		)
		if err != nil {
			return err
		}

		if err := taskGenerator.GenerateChildWorkflowTasks(
			scheduleEvent,
		); err != nil {
			return err
		}
	}

	return nil
}

func (r *TaskRefresherImpl) refreshTasksForRequestCancelExternalWorkflow(
	ctx context.Context,
	mutableState MutableState,
	taskGenerator TaskGenerator,
) error {

	executionInfo := mutableState.GetExecutionInfo()
	executionState := mutableState.GetExecutionState()
	pendingRequestCancelInfos := mutableState.GetPendingRequestCancelExternalInfos()

	currentBranchToken, err := mutableState.GetCurrentBranchToken()
	if err != nil {
		return err
	}

	for _, requestCancelInfo := range pendingRequestCancelInfos {
		initiateEvent, err := r.eventsCache.GetEvent(
			ctx,
			events.EventKey{
				NamespaceID: namespace.ID(executionInfo.NamespaceId),
				WorkflowID:  executionInfo.WorkflowId,
				RunID:       executionState.RunId,
				EventID:     requestCancelInfo.GetInitiatedEventId(),
				Version:     requestCancelInfo.GetVersion(),
			},
			requestCancelInfo.GetInitiatedEventBatchId(),
			currentBranchToken,
		)
		if err != nil {
			return err
		}

		if err := taskGenerator.GenerateRequestCancelExternalTasks(
			initiateEvent,
		); err != nil {
			return err
		}
	}

	return nil
}

func (r *TaskRefresherImpl) refreshTasksForSignalExternalWorkflow(
	ctx context.Context,
	mutableState MutableState,
	taskGenerator TaskGenerator,
) error {

	executionInfo := mutableState.GetExecutionInfo()
	executionState := mutableState.GetExecutionState()
	pendingSignalInfos := mutableState.GetPendingSignalExternalInfos()

	currentBranchToken, err := mutableState.GetCurrentBranchToken()
	if err != nil {
		return err
	}

	for _, signalInfo := range pendingSignalInfos {
		initiateEvent, err := r.eventsCache.GetEvent(
			ctx,
			events.EventKey{
				NamespaceID: namespace.ID(executionInfo.NamespaceId),
				WorkflowID:  executionInfo.WorkflowId,
				RunID:       executionState.RunId,
				EventID:     signalInfo.GetInitiatedEventId(),
				Version:     signalInfo.GetVersion(),
			},
			signalInfo.GetInitiatedEventBatchId(),
			currentBranchToken,
		)
		if err != nil {
			return err
		}

		if err := taskGenerator.GenerateSignalExternalTasks(
			initiateEvent,
		); err != nil {
			return err
		}
	}

	return nil
}

func (r *TaskRefresherImpl) refreshTasksForWorkflowSearchAttr(
	taskGenerator TaskGenerator,
) error {

	return taskGenerator.GenerateUpsertVisibilityTask()
}
