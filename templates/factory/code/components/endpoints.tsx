import { JSONView } from "./json-view";

export function Endpoints({
  endpoints,
}: {
  endpoints: Record<string, string>;
}) {
  return <JSONView>{JSON.stringify(endpoints, null, 2)}</JSONView>;
}
