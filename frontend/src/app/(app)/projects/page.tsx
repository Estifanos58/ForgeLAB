import { redirect } from 'next/navigation';

/**
 * Projects root route redirects seamlessly to Dashboard where
 * the projects list, project creation, and lifecycle operations live.
 */
export default function ProjectsPage() {
  redirect('/dashboard');
}
