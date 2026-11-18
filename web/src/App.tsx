import {
  Boxes,
  Cpu,
  Disc3,
  FileCode2,
  HardDrive,
  KeyRound,
  Layers,
  LayoutDashboard,
  ListChecks,
  Settings,
  UserCog,
  type LucideIcon,
} from "lucide-react";
import { lazy, Suspense } from "react";
import { NavLink, Route, Routes } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { Alert } from "@/components/ui/misc";
import { useServerEvents } from "@/lib/events";
import { cn } from "@/lib/utils";
import { BootImageDetailPage } from "@/pages/boot/BootImageDetail";
import { BootImagesPage } from "@/pages/boot/BootImages";
import { ClientDetailPage } from "@/pages/clients/ClientDetail";
import { ClientsPage } from "@/pages/clients/Clients";
import { DeploymentDetailPage } from "@/pages/deployments/DeploymentDetail";
import { DeploymentsPage } from "@/pages/deployments/Deployments";
import { ImageDetailPage } from "@/pages/images/ImageDetail";
import { ImagesPage } from "@/pages/images/Images";
import { Overview } from "@/pages/Overview";
import { Planned } from "@/pages/Planned";

// Loaded when first opened: the editor brings CodeMirror.
const TemplatesPage = lazy(() => import("@/pages/templates/Templates").then((m) => ({ default: m.TemplatesPage })));
const TemplateEditorPage = lazy(() => import("@/pages/templates/TemplateEditor").then((m) => ({ default: m.TemplateEditorPage })));
const DriverPackagesPage = lazy(() => import("@/pages/drivers/Packages").then((m) => ({ default: m.DriverPackagesPage })));
const DriverPackageDetailPage = lazy(() => import("@/pages/drivers/PackageDetail").then((m) => ({ default: m.DriverPackageDetailPage })));
const SettingsPage = lazy(() => import("@/pages/Settings").then((m) => ({ default: m.SettingsPage })));
const DriverGroupsPage = lazy(() => import("@/pages/drivers/Groups").then((m) => ({ default: m.DriverGroupsPage })));
const ProfilesPage = lazy(() => import("@/pages/profiles/Profiles").then((m) => ({ default: m.ProfilesPage })));
const SecretsPage = lazy(() => import("@/pages/secrets/Secrets").then((m) => ({ default: m.SecretsPage })));
const loading = <p className="text-muted-foreground">Loading…</p>;

type NavItem = { to: string; label: string; icon: LucideIcon; planned?: string };

// Order follows the setup flow: content first, then what uses it.
const nav: NavItem[] = [
  { to: "/", label: "Overview", icon: LayoutDashboard },
  { to: "/images", label: "Images", icon: Disc3 },
  { to: "/drivers", label: "Driver packages", icon: Cpu },
  { to: "/driver-groups", label: "Driver groups", icon: Layers },
  { to: "/boot-images", label: "Boot images", icon: HardDrive },
  { to: "/templates", label: "Unattend templates", icon: FileCode2 },
  { to: "/profiles", label: "Profiles", icon: Boxes },
  { to: "/clients", label: "Clients", icon: UserCog },
  { to: "/deployments", label: "Deployments", icon: ListChecks },
  { to: "/secrets", label: "Secrets", icon: KeyRound },
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
          <Route path="/drivers" element={<Suspense fallback={loading}><DriverPackagesPage /></Suspense>} />
          <Route path="/drivers/:driverPackageId" element={<Suspense fallback={loading}><DriverPackageDetailPage /></Suspense>} />
          <Route path="/driver-groups" element={<Suspense fallback={loading}><DriverGroupsPage /></Suspense>} />
          <Route path="/boot-images" element={<BootImagesPage />} />
          <Route path="/boot-images/:bootImageId" element={<BootImageDetailPage />} />
          <Route path="/templates" element={<Suspense fallback={loading}><TemplatesPage /></Suspense>} />
          <Route path="/templates/new" element={<Suspense fallback={loading}><TemplateEditorPage /></Suspense>} />
          <Route path="/templates/:templateId" element={<Suspense fallback={loading}><TemplateEditorPage /></Suspense>} />
          <Route path="/profiles" element={<Suspense fallback={loading}><ProfilesPage /></Suspense>} />
          <Route path="/clients" element={<ClientsPage />} />
          <Route path="/clients/:clientId" element={<ClientDetailPage />} />
          <Route path="/deployments" element={<DeploymentsPage />} />
          <Route path="/deployments/:deploymentId" element={<DeploymentDetailPage />} />
          <Route path="/secrets" element={<Suspense fallback={loading}><SecretsPage /></Suspense>} />
          <Route path="/settings" element={<Suspense fallback={loading}><SettingsPage /></Suspense>} />
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
