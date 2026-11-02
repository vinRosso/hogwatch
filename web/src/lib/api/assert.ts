// Type-level assertions on the generated schema. A line fails `npm run check`
// when the contract stops declaring the shape named in its comment.
import type { components } from "./schema";

type Equal<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;
type Assert<T extends true> = T;

type S = components["schemas"];

// Scan errors can hold host paths: only the admin view has one, and it is nullable.
export type _AdminError = Assert<Equal<S["ScanAdmin"]["error"], string | null>>;
export type _SummaryHasNoError = Assert<Equal<"error" extends keyof S["ScanSummary"] ? true : false, false>>;
export type _ProblemType = Assert<Equal<S["Problem"]["type"], string>>;
export type _CursorNullable = Assert<Equal<S["ChildrenPage"]["next_cursor"], string | null>>;
export type _MeNotice = Assert<Equal<S["Me"]["notice"], string | null>>;
export type _AuthConfig = Assert<Equal<S["AuthConfig"], { local: boolean; oidc: boolean }>>;

// M7: a filtered child has no logical size; nodes carry their root-relative path.
export type _ChildLogicalNullable = Assert<Equal<S["Child"]["bytes_logical"], number | null>>;
export type _NodeRelPath = Assert<Equal<S["Node"]["rel_path"], string>>;
export type _TreemapKind = Assert<Equal<S["TreemapNode"]["kind"], "dir" | "files" | "ext" | "rest">>;
export type _ListingTruncated = Assert<Equal<S["Listing"]["truncated"], boolean>>;
export type _ListingError = Assert<Equal<S["Listing"]["error"], string | null>>;

// M8: password is required on create; has_password shows OIDC-only accounts.
export type _UserInputPassword = Assert<Equal<S["UserInput"]["password"], string>>;
export type _AdminHasPassword = Assert<Equal<S["AdminUser"]["has_password"], boolean>>;
export type _PatchPassword = Assert<Equal<S["UserPatch"]["password"], string | undefined>>;
export type _IssueIdNullable = Assert<Equal<S["ConfigIssue"]["id"], number | null>>;
export type _ScanJobScan = Assert<Equal<S["ScanJob"]["scan_id"], number | null>>;
export type _MountRoot = Assert<Equal<S["AvailableMount"]["root_id"], number | null>>;
export type _RootInputNoHost = Assert<Equal<"host_path" extends keyof S["RootInput"] ? true : false, false>>;

// M9: growth is per root; null without a history point at or before the window.
export type _UsageGrowth = Assert<Equal<S["MyUsage"]["growth_bytes"], number | null>>;
export type _MountGrowth = Assert<Equal<S["MountStat"]["growth_bytes"], number | null>>;
