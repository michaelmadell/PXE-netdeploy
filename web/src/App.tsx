import {
  Boxes,
  Cpu,
  Disc3,
  FileCode2,
  HardDrive,
  KeyRound,
  LayoutDashboard,
  ListChecks,
  Settings,
  UserCog,
  type LucideIcon,
} from "lucide-react";
import { NavLink, Route, Routes } from "react-router";
import { cn } from "@/lib/utils";
import { Overview } from "@/pages/Overview";
import { Planned } from "@/pages/Planned";

type NavItem = { to: string; label: string; icon: LucideIcon; planned?: string };

// Order follows the setup flow: content first, then what uses it.
const nav: NavItem[] = [
  { to: "/", label: "Overview", icon: LayoutDashboard },
  { to: "/images", label: "Images", icon: Disc3, planned: "Upload ISOs, see editions and which boot managers each contains." },
  { to: "/drivers", label: "Drivers", icon: Cpu, planned: "Import driver packs, enable or disable individual INFs, and group them by hardware." },
  { to: "/boot-images", label: "Boot images", icon: HardDrive, planned: "Build WinPE images with drivers and the agent baked in." },
  { to: "/templates", label: "Unattend templates", icon: FileCode2, planned: "Edit and preview autounattend templates." },
  { to: "/profiles", label: "Profiles", icon: Boxes, planned: "Combine a boot image, an install image, drivers and identity settings." },
  { to: "/clients", label: "Clients", icon: UserCog, planned: "Register machines, approve discovered ones and assign profiles." },
  { to: "/deployments", label: "Deployments", icon: ListChecks, planned: "Schedule deployments and follow their progress and logs." },
  { to: "/secrets", label: "Secrets", icon: KeyRound, planned: "Store passwords, join credentials and Autopilot files. Values are never shown again." },
  { to: "/settings", label: "Settings", icon: Settings, planned: "Interfaces, DHCP mode, TFTP and HTTP listeners." },
];

export function App() {
  return (
    <div className="flex min-h-dvh">
      <aside className="w-60 shrink-0 border-r border-sidebar-border bg-sidebar text-sidebar-foreground">
        <div className="px-5 py-4 text-base font-semibold tracking-tight">netdeploy</div>
        <nav aria-label="Main">
          <ul className="space-y-0.5 px-2">
            {nav.map(({ to, label, icon: Icon }) => (
              <li key={to}>
                <NavLink
                  to={to}
                  end={to === "/"}
                  className={({ isActive }) =>
                    cn(
                      "flex items-center gap-2.5 rounded-md px-3 py-2 text-sm",
                      "hover:bg-sidebar-accent focus-visible:outline-2",
                      isActive && "bg-sidebar-accent font-medium",
                    )
                  }
                >
                  <Icon className="size-4 opacity-70" aria-hidden />
                  {label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
      </aside>

      <main className="min-w-0 flex-1 px-8 py-6">
        <Routes>
          <Route path="/" element={<Overview />} />
          {nav
            .filter((n) => n.planned)
            .map((n) => (
              <Route key={n.to} path={n.to} element={<Planned title={n.label} summary={n.planned!} />} />
            ))}
          <Route path="*" element={<Planned title="Page not found" summary="Use the menu to choose a section." />} />
        </Routes>
      </main>
    </div>
  );
}
