// Display names for registered protocol types (registry Catalog().protocols).
const labels: Record<string, string> = {
  rest: "REST",
  soap: "SOAP",
  graphql: "GraphQL",
  grpc: "gRPC",
};

const order = ["rest", "soap", "graphql", "grpc"];

/** The footer's protocol list: known protocols first in a fixed order, then
 * any plugin protocol by its type name, joined with " · ". */
export function protocolList(protocols: readonly string[] | null | undefined): string {
  const unique = [...new Set((protocols ?? []).filter((p) => p.trim() !== ""))];
  unique.sort((a, b) => {
    const ia = order.indexOf(a), ib = order.indexOf(b);
    if (ia >= 0 || ib >= 0) return (ia < 0 ? order.length : ia) - (ib < 0 ? order.length : ib);
    return a.localeCompare(b);
  });
  return unique.map((p) => labels[p] ?? p).join(" · ");
}
