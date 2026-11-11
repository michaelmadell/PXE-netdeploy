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
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { Alert } from "@/components/ui/misc";
import { useServerEvents } from "@/lib/events";
import { cn } from "@/lib/utils";
import { BootImageDetailPage } from "@/pages/boot/BootImageDetail";
import { BootImagesPage } from "@/pages/boot/BootImages";
import { ImageDetailPage } from "@/pages/images/ImageDetail";
import { ImagesPage } from "@/pages/images/Images";
import { Overview } from "@/pages/Overview";
import { Planned } from "@/pages/Planned";
import { ProfilesPage } from "@/pages/profiles/Profiles";
import { SettingsPage } from "@/pages/Settings";

type NavItem = { to: string; label: string; icon: LucideIcon; planned?: string };

// Order follows the setup flow: content first, then what uses it.
const nav: NavItem[] = [
  { to: "/", label: "Overview", icon: LayoutDashboard },
  { to: "/images", label: "Images", icon: Disc3 },
  { to: "/drivers", label: "Drivers", icon: Cpu, planned: "Import driver packs, enable or disable individual INFs, and group them by hardware." },
  { to: "/boot-images", label: "Boot images", icon: HardDrive },
  { to: "/templates", label: "Unattend templates", icon: FileCode2, planned: "Edit and preview autounattend templates." },
  { to: "/profiles", label: "Profiles", icon: Boxes },
  { to: "/clients", label: "Clients", icon: UserCog, planned: "Register machines, approve discovered ones and assign profiles." },
  { to: "/deployments", label: "Deployments", icon: ListChecks, planned: "Schedule deployments and follow their progress and logs." },
  { to: "/secrets", label: "Secrets", icon: KeyRound, planned: "Store passwords, join credentials and Autopilot files. Values are never shown again." },
  { to: "/settings", label: "Settings", icon: Settings },
];

export function App() {
  useServerEvents();
  // Shared with the Overview page's query; this one only reads the warnings.
  const health = useQuery({
    queryKey: ["health"],
    queryFn: async () => {
      const { data, error } = await api.GET("/system/health");
      return data ?? (error as NonNullable<typeof data>);
    },
  });
  const passwordOff = health.data?.warnings?.find((w) => w.code === "boot_prompt_password_disabled");

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
        {passwordOff && (
          <Alert tone="warning" className="mb-6 max-w-4xl" title="The boot prompt password is switched off">
            Anyone at a machine on a deployment network can re-image it. Turn “Require the password” back on in Settings
            when you have finished debugging.
          </Alert>
        )}
        <Routes>
          <Route path="/" element={<Overview />} />
          <Route path="/images" element={<ImagesPage />} />
          <Route path="/images/:imageId" element={<ImageDetailPage />} />
          <Route path="/boot-images" element={<BootImagesPage />} />
          <Route path="/boot-images/:bootImageId" element={<BootImageDetailPage />} />
          <Route path="/profiles" element={<ProfilesPage />} />
          <Route path="/settings" element={<SettingsPage />} />
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
