package protocolcore

import (
	"errors"
	"math"
	"unsafe"
)

// ResourceCost separates retained/copyable payload from materialized cells.
// Neither currency is a measurement of resident Go heap memory.
type ResourceCost struct{ PayloadBytes, StructureBytes uint64 }

type ResourceBudget struct{ limit, used ResourceCost }

func NewResourceBudget(limit ResourceCost) (*ResourceBudget, error) {
	if limit.PayloadBytes == 0 || limit.StructureBytes == 0 {
		return nil, errors.New("resource limits must be positive")
	}
	return &ResourceBudget{limit: limit}, nil
}

func (budget *ResourceBudget) Reserve(delta ResourceCost) error {
	if budget == nil {
		return errors.New("resource budget is nil")
	}
	if delta.PayloadBytes > budget.limit.PayloadBytes-budget.used.PayloadBytes || delta.StructureBytes > budget.limit.StructureBytes-budget.used.StructureBytes {
		return errors.New("resource budget exceeded")
	}
	budget.used.PayloadBytes += delta.PayloadBytes
	budget.used.StructureBytes += delta.StructureBytes
	return nil
}

type ResourceLimits struct{ Request, Response ResourceCost }

// ReserveNoticePaths checks generated path lengths before concatenating them.
// It reserves the temporary notice array and its immutable report copy.
func (budget *ResourceBudget) ReserveNoticePaths(code NoticeCode, prefix string, names []string) error {
	if budget == nil {
		return nil
	}
	var meter resourceMeter
	meter.cells(len(names), 2*unsafe.Sizeof(TranslationNotice{}))
	for _, name := range names {
		meter.strings(string(code), prefix, ".", name)
	}
	if meter.err != nil {
		return meter.err
	}
	return budget.Reserve(meter.cost)
}

// AppendReportWithin charges actual generated notice codes/paths and cells
// before the accumulator grows, including its final immutable freeze copy.
// Nil retains the legacy builder behavior.
func AppendReportWithin(builder *TranslationReportBuilder, report TranslationReport, budget *ResourceBudget) error {
	if budget != nil {
		var meter resourceMeter
		meter.cells(len(report.notices), 2*unsafe.Sizeof(TranslationNotice{}))
		for _, notice := range report.notices {
			meter.strings(string(notice.Code), notice.Path, string(notice.Code), notice.Path)
		}
		if meter.err != nil {
			return meter.err
		}
		if err := budget.Reserve(meter.cost); err != nil {
			return err
		}
	}
	builder.Append(report)
	return nil
}

func (limits ResourceLimits) Validate() error {
	if _, err := NewResourceBudget(limits.Request); err != nil {
		return err
	}
	_, err := NewResourceBudget(limits.Response)
	return err
}

// A meter counts every occurrence, including inactive tagged-union arms.
// Embedded field headers are already part of their owning cell's Sizeof;
// separately allocated pointees/slice elements and payload are added below.
// It neither invokes semantic validation nor copies payload to inspect it.
type resourceMeter struct {
	cost ResourceCost
	err  error
}

func (meter *resourceMeter) add(payload, structure uint64) {
	if meter.err != nil {
		return
	}
	if payload > math.MaxUint64-meter.cost.PayloadBytes || structure > math.MaxUint64-meter.cost.StructureBytes {
		meter.err = errors.New("resource accounting overflow")
		return
	}
	meter.cost.PayloadBytes += payload
	meter.cost.StructureBytes += structure
}

func (meter *resourceMeter) cells(count int, size uintptr) {
	if count < 0 || (size != 0 && uint64(count) > math.MaxUint64/uint64(size)) {
		meter.err = errors.New("resource accounting overflow")
		return
	}
	meter.add(0, uint64(count)*uint64(size))
}

func (meter *resourceMeter) strings(values ...string) {
	for _, value := range values {
		meter.add(uint64(len(value)), 0)
	}
}

func (meter *resourceMeter) agent(agent *AgentMessageContext) {
	if agent == nil {
		return
	}
	meter.cells(1, unsafe.Sizeof(*agent))
	meter.strings(agent.AgentName, agent.Author, agent.Recipient)
}

func (meter *resourceMeter) extension(extension ProviderExtension) {
	meter.strings(string(extension.source), string(extension.kind), extension.path)
	meter.cells(len(extension.fragments), unsafe.Sizeof([]byte(nil)))
	for _, fragment := range extension.fragments {
		meter.add(uint64(len(fragment)), 0)
	}
}

func (meter *resourceMeter) blocks(blocks []ContentBlock) {
	meter.cells(len(blocks), unsafe.Sizeof(ContentBlock{}))
	for _, block := range blocks {
		meter.strings(string(block.Kind), block.Text, block.Refusal,
			string(block.ToolCall.Kind), block.ToolCall.Key.source, block.ToolCall.Key.wireID,
			block.ToolCall.ItemKey.source, block.ToolCall.ItemKey.wireID,
			block.ToolCall.Namespace, block.ToolCall.Name, block.ToolCall.Input,
			block.ToolResult.Key.source, block.ToolResult.Key.wireID,
			block.ToolResult.Namespace, block.ToolResult.Name, block.ToolResult.Content)
		meter.add(uint64(len(block.ToolCall.Arguments.value)), 0)
		meter.extension(block.ProviderExtension)
		meter.agent(block.Agent)
	}
}

func (meter *resourceMeter) tools(tools []ToolDefinition) {
	meter.cells(len(tools), unsafe.Sizeof(ToolDefinition{}))
	for _, tool := range tools {
		meter.strings(string(tool.Kind), tool.Name, tool.Description, tool.NativeType,
			string(tool.CustomFormat.Kind), tool.CustomFormat.Syntax, tool.CustomFormat.Definition)
		meter.add(uint64(len(tool.InputSchema.value)), 0)
	}
}

func (meter *resourceMeter) evidence(values []ProtocolEvidenceValue) {
	meter.cells(len(values), unsafe.Sizeof(ProtocolEvidenceValue{}))
	for _, value := range values {
		meter.strings(value.Name, value.Value)
	}
}

func MeasureRequest(request Request) (ResourceCost, error) {
	var meter resourceMeter
	meter.cells(1, unsafe.Sizeof(request))
	meter.strings(request.RequestedModel, request.EffectiveModel,
		string(request.ToolChoice.Mode), request.ToolChoice.Name,
		string(request.Reasoning.Thinking), string(request.Reasoning.Display),
		string(request.Reasoning.Context), string(request.Reasoning.Effort),
		string(request.Reasoning.Summary), string(request.Reasoning.Execution),
		request.Diagnostics.PreviousMessageID, string(request.Output.Kind), string(request.OutputVerbosity))
	meter.blocks(request.System)
	meter.cells(len(request.Messages), unsafe.Sizeof(Message{}))
	for _, message := range request.Messages {
		meter.strings(string(message.Role))
		meter.agent(message.Agent)
		meter.blocks(message.Blocks)
	}
	meter.tools(request.Tools)
	meter.cells(len(request.ToolNamespaces), unsafe.Sizeof(ToolNamespace{}))
	for _, namespace := range request.ToolNamespaces {
		meter.strings(namespace.Name, namespace.Description)
		meter.tools(namespace.Tools)
	}
	meter.cells(len(request.Context.Edits), unsafe.Sizeof(ContextEdit{}))
	for _, edit := range request.Context.Edits {
		meter.strings(string(edit.Kind))
	}
	meter.add(uint64(len(request.Output.Schema.value)), 0)
	if request.Temperature != nil {
		meter.cells(1, unsafe.Sizeof(*request.Temperature))
	}
	if request.TopP != nil {
		meter.cells(1, unsafe.Sizeof(*request.TopP))
	}
	meter.cells(len(request.StopSequences), unsafe.Sizeof(""))
	meter.strings(request.StopSequences...)
	meter.evidence(request.ProtocolEvidence)
	return meter.cost, meter.err
}

func MeasureResponse(response Response) (ResourceCost, error) {
	var meter resourceMeter
	meter.cells(1, unsafe.Sizeof(response))
	meter.strings(response.ID, response.RequestedModel, response.EffectiveModel, response.ReportedModel,
		string(response.StopReason), response.StopSequence,
		response.Usage.InputUncached.Source, response.Usage.CacheWrite.Source, response.Usage.CacheRead.Source,
		response.Usage.Output.Source, response.Usage.Reasoning.Source)
	meter.blocks(response.Blocks)
	meter.cells(len(response.ProviderExtensions), unsafe.Sizeof(ProviderExtension{}))
	for _, extension := range response.ProviderExtensions {
		meter.extension(extension)
	}
	meter.evidence(response.ProtocolEvidence)
	return meter.cost, meter.err
}

func reserveMeasured(cost ResourceCost, err error, limit ResourceCost) error {
	if err != nil {
		return err
	}
	budget, err := NewResourceBudget(limit)
	if err != nil {
		return err
	}
	return budget.Reserve(cost)
}

func ValidateRequestWithin(request Request, limits ResourceLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	cost, err := MeasureRequest(request)
	if err := reserveMeasured(cost, err, limits.Request); err != nil {
		return err
	}
	return request.validate(false)
}

func ValidateResponseWithin(response Response, limits ResourceLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	cost, err := MeasureResponse(response)
	if err := reserveMeasured(cost, err, limits.Response); err != nil {
		return err
	}
	return response.Validate()
}

func CloneRequestWithin(request Request, limits ResourceLimits) (Request, error) {
	if err := ValidateRequestWithin(request, limits); err != nil {
		return Request{}, err
	}
	return request.Clone(), nil
}

func WithEffectiveModelWithin(request Request, model string, limits ResourceLimits) (Request, error) {
	// Check the final shape, including a longer mapped model, before cloning.
	request.EffectiveModel = model
	return CloneRequestWithin(request, limits)
}

func CloneResponseWithin(response Response, limits ResourceLimits) (Response, error) {
	if err := ValidateResponseWithin(response, limits); err != nil {
		return Response{}, err
	}
	return response.Clone(), nil
}
