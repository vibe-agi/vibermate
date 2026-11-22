package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// encode is the encoder's second pass: one write of the plan into one
// pre-sized buffer. Keys and items have a fixed order, so equal requests give
// equal bytes; strings are escaped once with HTML escaping off (Claude's
// prompts are full of < and >); schemas are appended as the bytes the client
// sent. A body above limit fails here, before re-admission could refuse it
// with a misleading reason.
func (plan *requestPlan) encode(limit int) ([]byte, error) {
	out := make([]byte, 0, min(plan.sizeHint, limit+1))
	out = append(out, `{"model":`...)
	out = appendJSONString(out, plan.model)
	out = append(out, `,"stream":true,"store":false`...)
	if plan.includeEncrypted {
		out = append(out, `,"include":["reasoning.encrypted_content"]`...)
	}
	if plan.promptCacheKey != "" {
		out = append(out, `,"prompt_cache_key":`...)
		out = appendJSONString(out, plan.promptCacheKey)
	}
	if plan.maxOutputTokens > 0 {
		out = append(out, `,"max_output_tokens":`...)
		out = strconv.AppendInt(out, int64(plan.maxOutputTokens), 10)
	}
	var err error
	if plan.temperature != nil {
		out = append(out, `,"temperature":`...)
		if out, err = appendJSONNumber(out, *plan.temperature); err != nil {
			return nil, err
		}
	}
	if plan.topP != nil {
		out = append(out, `,"top_p":`...)
		if out, err = appendJSONNumber(out, *plan.topP); err != nil {
			return nil, err
		}
	}
	if plan.effort != "" || plan.summary != "" {
		out = append(out, `,"reasoning":{`...)
		separator := ""
		if plan.effort != "" {
			out = append(out, `"effort":`...)
			out = appendJSONString(out, plan.effort)
			separator = ","
		}
		if plan.summary != "" {
			out = append(out, separator+`"summary":`...)
			out = appendJSONString(out, plan.summary)
		}
		out = append(out, '}')
	}
	if plan.format != nil || plan.verbosity != "" {
		out = append(out, `,"text":{`...)
		if plan.format != nil {
			out = append(out, `"format":{"type":"json_schema","name":`...)
			out = appendJSONString(out, plan.format.name)
			out = append(out, `,"schema":`...)
			out = append(out, plan.format.schema...)
			out = append(out, `,"strict":`...)
			out = strconv.AppendBool(out, plan.format.strict)
			out = append(out, '}')
		}
		if plan.verbosity != "" {
			if plan.format != nil {
				out = append(out, ',')
			}
			out = append(out, `"verbosity":`...)
			out = appendJSONString(out, plan.verbosity)
		}
		out = append(out, '}')
	}
	if len(plan.tools) != 0 {
		out = append(out, `,"tool_choice":`...)
		if plan.namedTool != "" {
			out = append(out, `{"type":"function","name":`...)
			out = appendJSONString(out, plan.namedTool)
			out = append(out, '}')
		} else {
			out = appendJSONString(out, plan.toolChoice)
		}
		out = append(out, `,"parallel_tool_calls":`...)
		out = strconv.AppendBool(out, plan.parallelToolCalls)
		out = append(out, `,"tools":[`...)
		for index, tool := range plan.tools {
			if index > 0 {
				out = append(out, ',')
			}
			out = append(out, `{"type":"function","name":`...)
			out = appendJSONString(out, tool.name)
			if tool.description != "" {
				out = append(out, `,"description":`...)
				out = appendJSONString(out, tool.description)
			}
			out = append(out, `,"parameters":`...)
			out = append(out, tool.parameters...)
			out = append(out, `,"strict":`...)
			out = strconv.AppendBool(out, tool.strict)
			out = append(out, '}')
		}
		out = append(out, ']')
	}
	if plan.hasInstructions {
		out = append(out, `,"instructions":"`...)
		for _, text := range plan.instructions {
			out = appendJSONStringBody(out, text)
		}
		out = append(out, '"')
	}
	out = append(out, `,"input":[`...)
	extensions := extensionTally{}
	for index := range plan.items {
		if index > 0 {
			out = append(out, ',')
		}
		item := &plan.items[index]
		if out, err = plan.appendItem(out, item, &extensions); err != nil {
			return nil, err
		}
		if len(out) > limit {
			return nil, oversizedBody(item.path())
		}
	}
	out = append(out, "]}"...)
	if len(out) > limit {
		return nil, oversizedBody("$")
	}
	return out, nil
}

func oversizedBody(path string) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonInvalidClientRequest,
		path,
		errors.New("encoded provider request exceeds the configured wire limit"),
	)
}

// extensionTally counts the bytes re-admission holds as provider extensions:
// each user-message attachment and each replayed reasoning item's summary,
// content and encrypted content, bounded together per request. Values that
// fit the client's own limits can exceed it once wrapped, and the encoder
// fails first, at the item's path.
type extensionTally struct {
	bytes int
}

func (tally *extensionTally) add(item *planItem, written int) error {
	tally.bytes += written
	if tally.bytes > protocolcore.MaxProviderExtensionBytes {
		return protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			item.path(),
			errors.New("encoded attachments and reasoning state exceed the provider's request limit"),
		)
	}
	return nil
}

// path names the neutral message an item came from.
func (item *planItem) path() string {
	if item.message < 0 {
		return "$.system"
	}
	return fmt.Sprintf("$.messages[%d]", item.message)
}

func (plan *requestPlan) appendItem(out []byte, item *planItem, extensions *extensionTally) ([]byte, error) {
	switch item.kind {
	case planItemMessage:
		out = append(out, `{"type":"message","role":`...)
		out = appendJSONString(out, item.role)
		if item.phase != "" {
			out = append(out, `,"phase":`...)
			out = appendJSONString(out, item.phase)
		}
		out = append(out, `,"content":[`...)
		for index := range item.parts {
			if index > 0 {
				out = append(out, ',')
			}
			start := len(out)
			out = plan.appendPart(out, &item.parts[index])
			if item.role == "user" && item.parts[index].media != nil {
				if err := extensions.add(item, len(out)-start); err != nil {
					return nil, err
				}
			}
		}
		return append(out, "]}"...), nil
	case planItemFunctionCall:
		out = append(out, `{"type":"function_call","call_id":`...)
		out = appendJSONString(out, item.callID)
		out = append(out, `,"name":`...)
		out = appendJSONString(out, item.name)
		out = append(out, `,"arguments":`...)
		out = appendJSONBytesString(out, item.arguments)
		return append(out, '}'), nil
	case planItemFunctionCallOutput:
		out = append(out, `{"type":"function_call_output","call_id":`...)
		out = appendJSONString(out, item.callID)
		out = append(out, `,"output":`...)
		if !item.outputList {
			out = appendTextValue(out, &item.parts[0])
			return append(out, '}'), nil
		}
		out = append(out, '[')
		for index := range item.parts {
			if index > 0 {
				out = append(out, ',')
			}
			out = plan.appendPart(out, &item.parts[index])
		}
		return append(out, "]}"...), nil
	case planItemReasoning:
		out = append(out, `{"type":"reasoning","id":`...)
		out = appendJSONString(out, item.reasoningID)
		out = append(out, `,"summary":[`...)
		if item.summary != "" {
			start := len(out)
			out = append(out, `{"type":"summary_text","text":`...)
			out = appendJSONString(out, item.summary)
			out = append(out, '}')
			if err := extensions.add(item, len(out)-start); err != nil {
				return nil, err
			}
		}
		out = append(out, ']')
		if item.contentSource && item.content != "" {
			out = append(out, `,"content":[`...)
			start := len(out)
			out = append(out, `{"type":"reasoning_text","text":`...)
			out = appendJSONString(out, item.content)
			out = append(out, '}')
			if err := extensions.add(item, len(out)-start); err != nil {
				return nil, err
			}
			out = append(out, ']')
		}
		out = append(out, `,"encrypted_content":`...)
		start := len(out)
		out = appendJSONString(out, item.encrypted)
		if err := extensions.add(item, len(out)-start); err != nil {
			return nil, err
		}
		return append(out, '}'), nil
	default:
		return nil, errors.New("planned item kind is invalid")
	}
}

func (plan *requestPlan) appendPart(out []byte, part *planPart) []byte {
	switch part.kind {
	case planPartInputText:
		out = append(out, `{"type":"input_text","text":`...)
		out = appendTextValue(out, part)
	case planPartOutputText:
		out = append(out, `{"type":"output_text","text":`...)
		out = appendTextValue(out, part)
	case planPartRefusal:
		out = append(out, `{"type":"refusal","refusal":`...)
		out = appendTextValue(out, part)
	case planPartInputImage:
		out = append(out, `{"type":"input_image","image_url":"`...)
		out = appendMediaReference(out, part.media)
		out = append(out, '"')
		if plan.imageDetail != "" {
			out = append(out, `,"detail":`...)
			out = appendJSONString(out, plan.imageDetail)
		}
	case planPartInputFile:
		if part.media.Data != "" {
			out = append(out, `{"type":"input_file","file_data":"`...)
			out = appendMediaReference(out, part.media)
			out = append(out, `","filename":`...)
			out = appendJSONString(out, part.filename)
		} else {
			out = append(out, `{"type":"input_file","file_url":`...)
			out = appendJSONString(out, part.media.URL)
		}
	}
	return append(out, '}')
}

// appendTextValue writes a text part's prefix, text and suffix as one string.
func appendTextValue(out []byte, part *planPart) []byte {
	out = append(out, '"')
	out = appendJSONStringBody(out, part.prefix)
	out = appendJSONStringBody(out, part.text)
	out = appendJSONStringBody(out, part.suffix)
	return append(out, '"')
}

// appendMediaReference writes inline media as a data URL, copying the base64
// payload once and never decoding it, or the media's https URL.
func appendMediaReference(out []byte, media *protocolcore.Media) []byte {
	if media.Data == "" {
		return appendJSONStringBody(out, media.URL)
	}
	out = append(out, "data:"...)
	out = appendJSONStringBody(out, media.MediaType)
	out = append(out, ";base64,"...)
	return appendJSONStringBody(out, media.Data)
}

func appendJSONNumber(out []byte, value float64) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	return append(out, encoded...), nil
}

// appendJSONString writes value as a JSON string.
func appendJSONString(out []byte, value string) []byte {
	out = append(out, '"')
	out = appendJSONStringBody(out, value)
	return append(out, '"')
}

// appendJSONBytesString writes bytes, such as a tool call's argument JSON, as
// a JSON string without copying them first.
func appendJSONBytesString(out []byte, value []byte) []byte {
	if len(value) == 0 {
		return append(out, `""`...)
	}
	return appendJSONString(out, unsafe.String(unsafe.SliceData(value), len(value)))
}

const hexDigits = "0123456789abcdef"

// appendJSONStringBody escapes value exactly as encoding/json does with HTML
// escaping off: quotes, backslashes and control characters, U+2028 and
// U+2029 (which some JavaScript parsers treat as line ends), and invalid
// UTF-8 as U+FFFD. Runs that need no escape are copied whole.
func appendJSONStringBody(out []byte, value string) []byte {
	start := 0
	for index := 0; index < len(value); {
		character := value[index]
		if character < utf8.RuneSelf {
			if character >= 0x20 && character != '"' && character != '\\' {
				index++
				continue
			}
			out = append(out, value[start:index]...)
			switch character {
			case '"', '\\':
				out = append(out, '\\', character)
			case '\b':
				out = append(out, '\\', 'b')
			case '\f':
				out = append(out, '\\', 'f')
			case '\n':
				out = append(out, '\\', 'n')
			case '\r':
				out = append(out, '\\', 'r')
			case '\t':
				out = append(out, '\\', 't')
			default:
				out = append(out, '\\', 'u', '0', '0', hexDigits[character>>4], hexDigits[character&0xF])
			}
			index++
			start = index
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[index:])
		if runeValue == utf8.RuneError && size == 1 {
			out = append(out, value[start:index]...)
			out = append(out, `\ufffd`...)
			index += size
			start = index
			continue
		}
		if runeValue == '\u2028' || runeValue == '\u2029' {
			out = append(out, value[start:index]...)
			out = append(out, '\\', 'u', '2', '0', '2', hexDigits[runeValue&0xF])
			index += size
			start = index
			continue
		}
		index += size
	}
	return append(out, value[start:]...)
}
