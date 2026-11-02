// Inline stroke icons (24×24 grid, currentColor). Decorative by default;
// pass a label to make one meaningful to screen readers.

import type { ReactNode } from "react";

type IconProps = { size?: number; label?: string; strokeWidth?: number };

function svg(paths: ReactNode) {
  return function Icon({ size = 16, label, strokeWidth = 2 }: IconProps) {
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
        role={label ? "img" : undefined}
        aria-label={label}
        aria-hidden={label ? undefined : true}
      >
        {paths}
      </svg>
    );
  };
}

export const Logo = svg(<><path d="M4 5l8 14 8-14" /><path d="M9 5l3 5 3-5" /></>);
export const Layers = svg(<><path d="M12 3l9 5-9 5-9-5z" /><path d="M3 13l9 5 9-5" /></>);
export const Globe = svg(<><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3c3 3.5 3 14.5 0 18M12 3c-3 3.5-3 14.5 0 18" /></>);
export const Clock = svg(<><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>);
export const Download = svg(<><path d="M12 3v12M7 10l5 5 5-5" /><path d="M4 19h16" /></>);
export const Play = svg(<path d="M7 4l12 8-12 8z" />);
export const Stop = svg(<rect x="6" y="6" width="12" height="12" rx="1" />);
export const Search = svg(<><circle cx="11" cy="11" r="7" /><path d="M20 20l-3.5-3.5" /></>);
export const Plus = svg(<path d="M12 5v14M5 12h14" />);
export const Close = svg(<path d="M6 6l12 12M18 6L6 18" />);
export const Check = svg(<path d="M5 12l5 5 9-10" />);
export const ChevronDown = svg(<path d="M6 9l6 6 6-6" />);
export const ChevronRight = svg(<path d="M9 6l6 6-6 6" />);
export const Lock = svg(<><rect x="5" y="11" width="14" height="10" rx="2" /><path d="M8 11V8a4 4 0 018 0v3" /></>);
export const Shield = svg(<path d="M12 3l8 4v5c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V7z" />);
export const Copy = svg(<><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M5 15V5a2 2 0 012-2h10" /></>);
export const File = svg(<><path d="M14 3H7a2 2 0 00-2 2v14a2 2 0 002 2h10a2 2 0 002-2V8z" /><path d="M14 3v5h5" /></>);
export const Folder = svg(<path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z" />);
export const Retry = svg(<><path d="M3 12a9 9 0 0115.5-6.2L21 8" /><path d="M21 3v5h-5" /></>);
export const Alert = svg(<><circle cx="12" cy="12" r="9" /><path d="M12 7v6M12 16.5h.01" /></>);
export const Info = svg(<><circle cx="12" cy="12" r="9" /><path d="M12 8h.01M11 12h1v5h1" /></>);
export const Braces = svg(<path d="M8 4H6a2 2 0 00-2 2v4l-2 2 2 2v4a2 2 0 002 2h2M16 4h2a2 2 0 012 2v4l2 2-2 2v4a2 2 0 01-2 2h-2" />);
export const Code = svg(<path d="M7 8l-4 4 4 4M17 8l4 4-4 4M14 4l-4 16" />);
export const Sun = svg(<><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></>);
export const Moon = svg(<path d="M21 12.8A9 9 0 1111.2 3a7 7 0 009.8 9.8z" />);
export const Sliders = svg(<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0M16 4v4M10 10v4M18 16v4" />);
export const Filter = svg(<path d="M4 6h16M7 12h10M10 18h4" />);
export const GitBranch = svg(<><circle cx="6" cy="6" r="2" /><circle cx="6" cy="18" r="2" /><circle cx="18" cy="12" r="2" /><path d="M6 8v8M8 6c4 0 8 2 8 6" /></>);
export const Save = svg(<><path d="M5 3h11l3 3v13a2 2 0 01-2 2H7a2 2 0 01-2-2z" /><path d="M8 3v5h7M8 21v-7h8v7" /></>);
export const Gauge = svg(<><path d="M4.9 19.1a10 10 0 1114.2 0" /><path d="M12 12l4-4" /><circle cx="12" cy="12" r="1.5" /></>);
