package protocolcore

// TranslationReportBuilder accumulates notices for one call. Its zero value is
// usable; a builder must not be copied after use or shared across goroutines.
// Build returns an immutable snapshot independent of subsequent appends.
type TranslationReportBuilder struct {
	notices []TranslationNotice
}

func (builder *TranslationReportBuilder) Append(report TranslationReport) {
	builder.notices = append(builder.notices, report.notices...)
}

func (builder *TranslationReportBuilder) Build() TranslationReport {
	return NewTranslationReport(builder.notices...)
}
