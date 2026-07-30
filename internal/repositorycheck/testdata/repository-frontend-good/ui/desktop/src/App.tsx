type Properties = {
  readonly translate: (key: string) => string;
};

export function App({ translate }: Properties) {
  return <button aria-label={translate("approval.allowOnce.action")}>{translate("approval.allowOnce.action")}</button>;
}
