package codesignature

// The one generator. Requirement syntax is permitted here and nowhere else.
const template = `identifier "%s" and anchor apple generic ` +
	`and certificate 1[field.1.2.840.113635.100.6.2.6] ` +
	`and certificate leaf[field.1.2.840.113635.100.6.1.13] ` +
	`and certificate leaf[subject.OU] = "%s"`
