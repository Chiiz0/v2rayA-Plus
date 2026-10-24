import type { Touch, Which } from "@/api/types";

export function nodeKey(which: Which | null | undefined): string {
  if (!which) return "";
  return which._type === "subscriptionServer"
    ? `subscriptionServer:${which.sub}:${which.id}`
    : `server:${which.id}`;
}
export interface NodeOption {
  value: string;
  title: string;
  which: Which;
}
export function nodeOptions(touch: Touch, local = "Local"): NodeOption[] {
  const direct: NodeOption[] = touch.servers.map((s) => ({
    value: nodeKey(s),
    title: `${local} · ${s.name} (${s.address})`,
    which: { _type: "server" as const, id: s.id },
  }));
  return direct.concat(
    touch.subscriptions.flatMap((subscription, sub) =>
      subscription.servers.map((s) => {
        const which: Which = { _type: "subscriptionServer", id: s.id, sub };
        return {
          value: nodeKey(which),
          title: `${subscription.remarks || subscription.host} · ${s.name} (${s.address})`,
          which,
        };
      }),
    ),
  );
}
