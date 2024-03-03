import Link from "next/link";

type SidebarProps = {
  loading: boolean;
};

export const Sidebar = () => {
  return (
    <aside className="p-[0.2rem] bg-[#333] text-white">
      <nav className="max-w-[42rem] my-[1.5rem] mx-auto">
        <ul>
          <li>
            <Link href="/">Home</Link>
          </li>
          <li>
            <Link href="/about">About</Link>
          </li>
          <li>
            <Link href="/advanced/api-profile">
              API rendered profile (advanced)
            </Link>
          </li>
          <li>
            <Link href="/profile">Client rendered profile</Link>
          </li>
          <li>
            <Link href="/advanced/ssr-profile">
              Server rendered profile (advanced)
            </Link>
          </li>
        </ul>
      </nav>
    </aside>
  );
};
