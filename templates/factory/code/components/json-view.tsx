import { PropsWithChildren } from "react";
import JsonFormatter from "react-json-formatter";

export function JSONView({ children }: PropsWithChildren) {
  return (
    <pre>
      <JsonFormatter json={children.toString()} tabWith={2} />
    </pre>
  );
}
