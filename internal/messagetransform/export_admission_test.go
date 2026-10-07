package messagetransform

import (
	"context"
	"math"
	"net/http"
	"runtime"
	"testing"

	"github.com/dop251/goja"
)

func TestBodyExportRejectsBeforeUnicodeConversion(t *testing.T) {
	vm := goja.New()
	value, err := vm.RunString(`({body:"λ".repeat(1024*1024),headers:{}})`)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaximumBodyBytes = 64 << 10
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = exportMessage(vm, value, scriptMessage{}, limits)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("oversized body accepted")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("oversized Unicode body converted before bound: %d bytes", allocated)
	}
}

func TestTransformRetainedEnvelopeRejectsArithmeticOverflow(t *testing.T) {
	limits := DefaultLimits()
	limits.MaximumHeaderFields = math.MaxInt
	pipeline, err := CompilePipeline([]Policy{{RequestJavaScript: ";"}}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pipeline.RetainedRequestBytes(); err == nil {
		t.Fatal("overflowed transform graph accepted")
	}
}

func TestScriptExportUnicodeBoundaries(t *testing.T) {
	vm := goja.New()
	for _, test := range []struct {
		source string
		limit  int
		valid  bool
	}{{`"λ"`, 2, true}, {`"λ"`, 1, false}, {`"🙂"`, 4, true}, {`"🙂"`, 3, false}, {`"\ufffd"`, 3, true}, {`"\ud800"`, 3, false}, {`"\udc00"`, 3, false}} {
		value, err := vm.RunString(test.source)
		if err != nil {
			t.Fatal(err)
		}
		_, err = scriptStringWithin(value, test.limit)
		if (err == nil) != test.valid {
			t.Fatalf("%s/%d: %v", test.source, test.limit, err)
		}
	}
}
func TestScriptExportPoisonProxyCycleAndAggregate(t *testing.T) {
	for _, test := range []struct {
		name, source string
		valid        bool
	}{
		{"poisoned_intrinsic", `Object.keys=()=>[];Object.getOwnPropertyNames=()=>[];context.kept="yes";request.headers["x-kept"]="yes";`, true},
		{"proxy", `context.item=new Proxy({kept:"yes"},{ownKeys(t){return Reflect.ownKeys(t)}});`, true},
		{"cycle", `context.self=context;`, false},
		{"header_aggregate", `request.headers={a:"x".repeat(16000),b:"x".repeat(16000),c:"x".repeat(16000),d:"x".repeat(16000),e:"x".repeat(16000)};`, false},
		{"context_aggregate", `context.a="x".repeat(40000);context.b="x".repeat(40000);`, false},
		{"getter", `Object.defineProperty(context,"kept",{enumerable:true,get(){return "yes"}});`, true},
		{"proxy_overflow", `context.item=new Proxy({a:1},{ownKeys(){return Array.from({length:2000},(_,i)=>"k"+i)},getOwnPropertyDescriptor(){return {enumerable:true,configurable:true,value:1}}});`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			program, err := Compile(Policy{RequestJavaScript: test.source}, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			turn := program.NewTurn()
			output, err := turn.ApplyRequest(context.Background(), RequestMessage{Method: "POST", Path: "/v1/messages", Headers: http.Header{}, Body: []byte(`{"model":"m"}`)})
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
			if test.name == "poisoned_intrinsic" && (output.Headers.Get("X-Kept") != "yes" || string(turn.context) != `{"kept":"yes"}`) {
				t.Fatal("poisoned global changed owned key semantics")
			}
		})
	}
}

func TestContextExportChecksStructureBeforeUnadmittedGetters(t *testing.T) {
	vm := goja.New()
	value, err := vm.RunString(`var visits=0;({a:1,get b(){visits++;return "late";}})`)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaximumContextValues = 2
	if _, err = exportContext(vm, value, limits); err == nil {
		t.Fatal("oversized context accepted")
	}
	if got := vm.Get("visits").ToInteger(); got != 0 {
		t.Fatalf("unadmitted graph recursively exported: getter visits=%d", got)
	}
}
